// Package extension is the host side of the Agent Extension Protocol.
// Process runs one extension, and Set keeps those the user registered and
// gives the agent their tools.
package extension

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LiasCode/rumbo/internal/procgroup"
	"github.com/LiasCode/rumbo/protocol"
)

// How long an extension gets to answer a shutdown.
const gracePeriod = 5 * time.Second

// Process is an extension process spoken to over its stdin and stdout. One
// process serves every run of the host, so that what it keeps, such as a
// connection, lasts from one call to the next; calls may therefore be in
// progress at once, and each response is matched to its request by id.
type Process struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	// exited is closed once the extension's stdout has closed.
	exited chan struct{}
	// tools, sections, events, and functions are what the extension
	// declared on initialize.
	tools     []protocol.ToolDef
	sections  []string
	events    []string
	functions []protocol.Function
	// stale is set once the extension said what it declared has changed.
	stale atomic.Bool
	// outbox holds the events the extension is still to be told, and flush
	// asks for all of them to be written, which closing waits for.
	outbox chan protocol.Event
	flush  chan chan struct{}

	mu     sync.Mutex
	nextID int64
	// waiting are the requests sent and not answered yet, by id.
	waiting map[int64]*waiting
}

// waiting is a request the extension has not answered yet.
type waiting struct {
	answer chan protocol.Frame
	// ctx ends when the request is given up.
	ctx context.Context
	// host is what the host does for the extension while it works on the
	// request.
	host Host
}

// Host is what the host does for an extension while it answers a call or a
// function. Each is nil when there is nothing to do it with: a function is
// in no run, and has no tools; work nobody follows has no one to tell.
type Host struct {
	// Tool runs one of rumbo's own tools.
	Tool func(ctx context.Context, name string, args json.RawMessage) (protocol.CallResult, error)
	// Say shows the person what the extension says.
	Say func(text string)
	// Ask asks the person something and waits for the answer.
	Ask func(ctx context.Context, question string, secret bool) (string, error)
}

// serve answers a request the extension makes of the host.
func (h Host) serve(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	switch {
	case method == protocol.MethodCall && h.Tool != nil:
		var asked protocol.HostCallParams

		if err := json.Unmarshal(raw, &asked); err != nil || asked.Name == "" {
			return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "call needs the id of the call it is made in, and the name of a tool"}
		}

		return h.Tool(ctx, asked.Name, asked.Args)

	case method == protocol.MethodCall:
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "rumbo's tools are called from inside a call, which a function is not"}

	case method == protocol.MethodAsk && h.Ask != nil:
		var asked protocol.AskParams

		if err := json.Unmarshal(raw, &asked); err != nil {
			return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "ask needs the id of what it is asked in, and a text"}
		}

		answer, err := h.Ask(ctx, asked.Text, asked.Secret)

		return protocol.AskResult{Text: answer}, err

	case method == protocol.MethodAsk:
		return nil, errors.New("there is no one to ask")
	}

	return nil, &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "rumbo does not offer " + method + " to an extension"}
}

// maxPending is how many events wait for an extension that is slow to read
// them. What comes after is dropped, so a run never waits for a listener.
const maxPending = 1024

// Start runs argv as the extension called name and performs the initialize
// handshake.
// The extension's stderr goes to stderr, so its diagnostics reach the user.
// Cancelling ctx only abandons the handshake; the process lives until
// Close.
func Start(ctx context.Context, name string, argv []string, config json.RawMessage, stderr io.Writer) (*Process, error) {
	if len(argv) == 0 {
		return nil, errors.New("extension command is empty")
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stderr = stderr
	// Its own process group keeps the terminal's Ctrl-C away from the
	// extension; the host cancels a call through the protocol instead.
	procgroup.Own(cmd)

	stdin, err := cmd.StdinPipe()

	if err != nil {
		return nil, err
	}

	stdout, err := cmd.StdoutPipe()

	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start extension %q: %w", argv[0], err)
	}

	process := &Process{cmd: cmd, stdin: stdin, exited: make(chan struct{}), waiting: map[int64]*waiting{}}

	go process.read(stdout)

	var result protocol.ExtensionInitializeResult
	params := protocol.InitializeParams{Protocol: protocol.ExtensionVersion, Config: config, Name: name}

	if err := process.request(ctx, protocol.MethodInitialize, params, &result, Host{}); err != nil {
		_ = process.Close()
		return nil, fmt.Errorf("initialize extension: %w", err)
	}

	if result.Protocol != protocol.ExtensionVersion {
		_ = process.Close()
		return nil, fmt.Errorf("extension %q speaks protocol %d, host speaks %d", result.Name, result.Protocol, protocol.ExtensionVersion)
	}

	process.tools, process.sections, process.events, process.functions = result.Tools, result.Sections, result.Events, result.Functions

	if len(process.events) > 0 {
		process.outbox, process.flush = make(chan protocol.Event, maxPending), make(chan chan struct{})

		go process.tell()
	}

	return process, nil
}

// Tools are the tools the extension declared when it started.
func (p *Process) Tools() []protocol.ToolDef {
	return p.tools
}

// Sections are the names of the sections the extension declared when it
// started.
func (p *Process) Sections() []string {
	return p.sections
}

// Section returns the text of a section of the extension.
func (p *Process) Section(ctx context.Context, params protocol.SectionParams) (string, error) {
	var result protocol.SectionResult
	err := p.request(ctx, protocol.MethodSection, params, &result, Host{})

	return result.Text, err
}

// Functions are the functions the extension declared when it started.
func (p *Process) Functions() []protocol.Function {
	return p.functions
}

// Call runs a tool of the extension. While it works, the extension may
// call rumbo's own tools and reach the person through host.
func (p *Process) Call(ctx context.Context, params protocol.CallParams, host Host) (protocol.CallResult, error) {
	var result protocol.CallResult
	err := p.request(ctx, protocol.MethodCall, params, &result, host)

	return result, err
}

// Invoke calls a function of the extension, which talks with the person
// through host while it works.
func (p *Process) Invoke(ctx context.Context, name string, params json.RawMessage, host Host) (json.RawMessage, error) {
	var result json.RawMessage
	err := p.request(ctx, protocol.MethodInvoke, protocol.InvokeParams{Name: name, Params: params}, &result, host)

	return result, err
}

// Tell queues an event for the extension, when it asked for events of that
// type. It never waits: an extension too slow to read misses what does not
// fit.
func (p *Process) Tell(event protocol.Event) {
	if !slices.Contains(p.events, event.Type) {
		return
	}

	select {
	case p.outbox <- event:
	default:
	}
}

// tell writes the queued events, in order, until the extension is gone.
func (p *Process) tell() {
	write := func(event protocol.Event) {
		p.mu.Lock()
		defer p.mu.Unlock()

		// A failed write means the extension is gone, and so is this loop.
		_ = p.write(protocol.Frame{Method: protocol.MethodEvent}, event)
	}

	for {
		select {
		case <-p.exited:
			return

		case event := <-p.outbox:
			write(event)

		case flushed := <-p.flush:
			for pending := true; pending; {
				select {
				case event := <-p.outbox:
					write(event)
				default:
					pending = false
				}
			}

			close(flushed)
		}
	}
}

// told waits until the extension was written every event queued for it, so
// that how the last run ended reaches it before it is asked to stop.
func (p *Process) told() {
	if p.flush == nil {
		return
	}

	flushed := make(chan struct{})
	patience := time.After(gracePeriod)

	select {
	case p.flush <- flushed:
	case <-p.exited:
		return
	case <-patience:
		return
	}

	select {
	case <-flushed:
	case <-p.exited:
	case <-patience:
	}
}

// Stale reports whether the extension said that what it declared when it
// started has changed, so it has to be started again to hear it.
func (p *Process) Stale() bool {
	return p.stale.Load()
}

// Exited reports whether the extension process has gone.
func (p *Process) Exited() bool {
	select {
	case <-p.exited:
		return true
	default:
		return false
	}
}

// Pid is the system's id of the extension's process.
func (p *Process) Pid() int {
	return p.cmd.Process.Pid
}

// Close asks the extension to shut down and kills it if it does not.
func (p *Process) Close() error {
	p.told()

	ctx, cancel := context.WithTimeout(context.Background(), gracePeriod)
	defer cancel()

	_ = p.request(ctx, protocol.MethodShutdown, struct{}{}, nil, Host{})
	_ = p.stdin.Close()

	done := make(chan error, 1)

	go func() {
		done <- p.cmd.Wait()
	}()

	select {
	case <-done:
	case <-time.After(gracePeriod):
		_ = p.cmd.Process.Kill()
		<-done
	}

	return nil
}

// request sends one request and waits for its response. When ctx ends first
// the extension is told to stop, and whatever it answers later is dropped.
// What the extension says and asks of the host meanwhile goes to host.
func (p *Process) request(ctx context.Context, method string, params, result any, host Host) error {
	answer := make(chan protocol.Frame, 1)

	// What the extension asked for on the way ends with the request.
	asking, stopAsking := context.WithCancel(ctx)
	defer stopAsking()

	p.mu.Lock()
	p.nextID++
	id := p.nextID
	p.waiting[id] = &waiting{answer: answer, ctx: asking, host: host}
	err := p.write(protocol.Frame{ID: &id, Method: method}, params)
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		delete(p.waiting, id)
		p.mu.Unlock()
	}()

	if err != nil {
		return err
	}

	var frame protocol.Frame

	select {
	case <-ctx.Done():
		p.mu.Lock()
		_ = p.write(protocol.Frame{Method: protocol.MethodCancel}, protocol.CancelParams{ID: id})
		p.mu.Unlock()

		return ctx.Err()

	case frame = <-answer:

	case <-p.exited:
		// The answer may have arrived just before the process went.
		select {
		case frame = <-answer:
		default:
			return errors.New("the extension process exited")
		}
	}

	if frame.Error != nil {
		return frame.Error
	}

	if result == nil {
		return nil
	}

	return json.Unmarshal(frame.Result, result)
}

// write sends one frame. The caller holds the lock, so frames do not mix.
func (p *Process) write(frame protocol.Frame, params any) error {
	raw, err := json.Marshal(params)

	if err != nil {
		return err
	}

	frame.JSONRPC, frame.Params = "2.0", raw

	line, err := json.Marshal(frame)

	if err != nil {
		return err
	}

	if _, err := p.stdin.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("the extension process could not be reached: %w", err)
	}

	return nil
}

// read gives each response to the request that waits for it, and takes
// what the extension says and asks, until the extension's stdout closes.
// Lines that are not JSON are skipped, so a stray print does not break the
// extension, and so is a notification it is not known to send.
func (p *Process) read(stdout io.Reader) {
	defer close(p.exited)

	reader := bufio.NewReader(stdout)

	for {
		line, err := reader.ReadBytes('\n')

		var frame protocol.Frame

		if len(line) > 0 && json.Unmarshal(line, &frame) == nil {
			switch {
			case frame.Method != "" && frame.ID != nil:
				go p.answer(frame)

			case frame.Method == protocol.MethodDelta:
				p.said(frame)

			case frame.Method == protocol.MethodChanged:
				p.stale.Store(true)

			case frame.ID != nil:
				// A second answer to one request finds its place taken.
				if asked := p.within(*frame.ID); asked != nil {
					select {
					case asked.answer <- frame:
					default:
					}
				}
			}
		}

		if err != nil {
			return
		}
	}
}

// within returns the request with id that the extension has not answered
// yet, or nil.
func (p *Process) within(id int64) *waiting {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.waiting[id]
}

// said passes on what the extension says while it works on a request, in
// the order it says it.
func (p *Process) said(frame protocol.Frame) {
	var delta protocol.DeltaParams

	if json.Unmarshal(frame.Params, &delta) != nil {
		return
	}

	if asked := p.within(delta.ID); asked != nil && asked.host.Say != nil {
		asked.host.Say(delta.Text)
	}
}

// answer replies to a request the extension made of the host. It belongs
// to the request of the host's that its params name by id, and is refused
// when that one is over or lets the extension ask nothing.
func (p *Process) answer(frame protocol.Frame) {
	var within struct {
		ID int64 `json:"id"`
	}

	_ = json.Unmarshal(frame.Params, &within)

	asked := p.within(within.ID)
	reply := protocol.Frame{JSONRPC: "2.0", ID: frame.ID}

	if asked == nil {
		reply.Error = &protocol.Error{Code: protocol.CodeInvalidParams, Message: frame.Method + " belongs to nothing in progress: give the id of the call or the function it is made in"}
	} else if result, err := asked.host.serve(asked.ctx, frame.Method, frame.Params); err != nil {
		reply.Error = &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}

		if failure, coded := errors.AsType[*protocol.Error](err); coded {
			reply.Error = failure
		}
	} else {
		reply.Result, _ = json.Marshal(result)
	}

	line, _ := json.Marshal(reply)

	p.mu.Lock()
	defer p.mu.Unlock()

	_, _ = p.stdin.Write(append(line, '\n'))
}
