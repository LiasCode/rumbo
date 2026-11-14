// Package frontend is the host side of the Agent Frontend Protocol. It turns
// a pair of streams into run requests, each with the Observer and Inbox the
// agent needs, so the loop never knows what kind of front end is attached.
package frontend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"rumbo/protocol"
)

// Handlers are what the host does for a front end.
type Handlers struct {
	// Run accepts a run request and returns the work to do, or why the run
	// cannot start. It is called as requests arrive, before the next one is
	// read, so what it decides holds for the requests after it.
	Run func(params protocol.RunParams, run *Run) (Work, error)
	// Describe says what the provider offers and what is in force.
	Describe func(ctx context.Context) (protocol.DescribeResult, error)
	// Invoke calls a function of the provider, which talks with the person
	// through dialog while it works.
	Invoke func(ctx context.Context, params protocol.InvokeParams, dialog protocol.Dialog) (json.RawMessage, error)
	// Host calls a function of the host.
	Host func(ctx context.Context, name string, params json.RawMessage) (protocol.HostResult, error)
	// Slow says which functions of the host reach a provider. Those, like
	// describe and the provider's own functions, are answered when they are
	// done, without holding up the requests that come after them.
	Slow func(name string) bool
}

// Work is an accepted run. It returns the session the run worked in.
type Work func(ctx context.Context) (session string, err error)

// Conn is a connected front end.
type Conn struct {
	reader *bufio.Reader
	// stopped ends Serve after the request being answered.
	stopped atomic.Bool

	mu  sync.Mutex
	out *json.Encoder

	// running guards what follows: the runs in progress and the cancels of
	// what is answered in the background, both by the id of their request,
	// and the questions sent to the front end and not answered yet, by the
	// id of the host's own request.
	running sync.Mutex
	runs    map[int64]*Run
	calls   map[int64]context.CancelFunc
	asked   map[int64]chan protocol.Frame
	nextID  int64

	// quit is closed to end Serve at once, as a disconnect would.
	quit     chan struct{}
	quitOnce sync.Once
}

// read is one frame from the front end, or why there are no more.
type read struct {
	frame protocol.Frame
	err   error
}

// Connect performs the initialize handshake over r and w.
func Connect(r io.Reader, w io.Writer, info protocol.FrontendInitializeParams) (*Conn, error) {
	conn := &Conn{
		reader: bufio.NewReader(r),
		out:    json.NewEncoder(w),
		runs:   map[int64]*Run{},
		calls:  map[int64]context.CancelFunc{},
		asked:  map[int64]chan protocol.Frame{},
		quit:   make(chan struct{}),
		nextID: 1,
	}

	id := conn.nextID
	info.Protocol = protocol.Version
	params, _ := json.Marshal(info)

	if err := conn.send(protocol.Frame{ID: &id, Method: protocol.MethodInitialize, Params: params}); err != nil {
		return nil, err
	}

	for {
		frame, err := conn.next()

		if err != nil {
			return nil, fmt.Errorf("front end did not answer initialize: %w", err)
		}

		if frame.ID == nil || *frame.ID != id || frame.Method != "" {
			continue
		}

		if frame.Error != nil {
			return nil, frame.Error
		}

		var result protocol.InitializeResult

		if err := json.Unmarshal(frame.Result, &result); err != nil {
			return nil, err
		}

		if result.Protocol != protocol.Version {
			return nil, fmt.Errorf("front end %q speaks protocol %d, host speaks %d", result.Name, result.Protocol, protocol.Version)
		}

		return conn, nil
	}
}

// Serve handles the front end's requests until it disconnects or Shutdown
// is called. Runs in progress at that point are cancelled and waited for,
// and so is what is being answered in the background.
func (c *Conn) Serve(handlers Handlers) error {
	var active sync.WaitGroup

	ctx, stop := context.WithCancel(context.Background())

	defer active.Wait()
	defer stop()
	defer c.cancel(protocol.CancelRunParams{})
	defer c.Shutdown()

	reads := c.reading()

	for {
		var frame protocol.Frame

		select {
		case <-c.quit:
			return nil

		case next := <-reads:
			if errors.Is(next.err, io.EOF) {
				return nil
			}

			if next.err != nil {
				return next.err
			}

			frame = next.frame
		}

		switch frame.Method {
		case protocol.MethodCancel:
			// A cancel that names nothing cancels every run.
			var params protocol.CancelRunParams
			_ = json.Unmarshal(frame.Params, &params)

			c.cancel(params)

		// The front end answers a question the host asked it.
		case "":
			if frame.ID != nil {
				c.answered(frame)
			}

		case protocol.MethodRun:
			c.start(frame, handlers, &active)

		case protocol.MethodSteer:
			c.steer(frame)

		case protocol.MethodDescribe, protocol.MethodInvoke:
			if c.slow(frame, handlers) {
				call, cancel := c.track(ctx, frame.ID)

				active.Go(func() {
					defer cancel()

					c.answer(call, frame, handlers)
				})

				continue
			}

			c.answer(ctx, frame, handlers)

			if c.stopped.Load() {
				return nil
			}

		default:
			c.fail(frame.ID, protocol.CodeMethodNotFound, "unknown method: "+frame.Method)
		}
	}
}

// start accepts a run request and works on it in the background.
func (c *Conn) start(frame protocol.Frame, handlers Handlers, active *sync.WaitGroup) {
	var params protocol.RunParams

	if frame.ID == nil || json.Unmarshal(frame.Params, &params) != nil || (params.Objective == "") != params.Resume {
		c.fail(frame.ID, protocol.CodeInvalidParams, "run needs an id and either an objective or resume")
		return
	}

	if params.New && (params.Session != "" || params.Resume) {
		c.fail(frame.ID, protocol.CodeInvalidParams, "a run in a new session takes an objective and no session")
		return
	}

	id := *frame.ID

	c.running.Lock()
	_, taken := c.runs[id]
	c.running.Unlock()

	if taken {
		c.fail(frame.ID, protocol.CodeInvalidParams, "a run with this id is in progress")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	run := &Run{conn: c, id: id, cancel: cancel, listening: true}
	work, err := handlers.Run(params, run)

	if err != nil {
		cancel()
		c.fail(frame.ID, protocol.CodeInvalidParams, err.Error())

		return
	}

	// Before the next frame is read, so a cancel or a steer sent right
	// after the run finds it.
	c.running.Lock()
	c.runs[id] = run
	c.running.Unlock()

	active.Add(1)

	go func() {
		defer active.Done()

		session, err := work(ctx)
		result := protocol.RunResult{Status: protocol.RunDone, Session: session}

		switch {
		case ctx.Err() != nil:
			result.Status = protocol.RunCancelled
		case err != nil:
			result.Status = protocol.RunFailed
			result.Error = err.Error()
		}

		run.deafen()

		c.running.Lock()
		delete(c.runs, id)
		c.running.Unlock()

		cancel()

		raw, _ := json.Marshal(result)
		_ = c.send(protocol.Frame{ID: frame.ID, Result: raw})
	}()
}

// cancel stops the function named by its invoke, or the run named by its
// request, or every run when it names neither.
func (c *Conn) cancel(params protocol.CancelRunParams) {
	c.running.Lock()
	defer c.running.Unlock()

	if params.Invoke != 0 {
		if cancel := c.calls[params.Invoke]; cancel != nil {
			cancel()
		}

		return
	}

	for id, run := range c.runs {
		if params.Run == 0 || params.Run == id {
			run.cancel()
		}
	}
}

// track gives a request answered in the background a context of its own,
// so a cancel can name it. The returned cancel also forgets it.
func (c *Conn) track(ctx context.Context, id *int64) (context.Context, context.CancelFunc) {
	call, cancel := context.WithCancel(ctx)

	if id == nil {
		return call, cancel
	}

	c.running.Lock()
	c.calls[*id] = cancel
	c.running.Unlock()

	return call, func() {
		c.running.Lock()
		delete(c.calls, *id)
		c.running.Unlock()

		cancel()
	}
}

// steer holds what the front end tells a run until the run takes it.
func (c *Conn) steer(frame protocol.Frame) {
	var params protocol.SteerParams

	if frame.ID == nil || json.Unmarshal(frame.Params, &params) != nil || strings.TrimSpace(params.Text) == "" {
		c.fail(frame.ID, protocol.CodeInvalidParams, "steer needs an id and a text")
		return
	}

	run, err := c.find(params.Run)

	if err != nil {
		c.fail(frame.ID, protocol.CodeInvalidParams, err.Error())
		return
	}

	run.hold(frame.ID, params.Text)
}

// find returns the run in progress whose request has id, or for zero the
// only one in progress.
func (c *Conn) find(id int64) (*Run, error) {
	c.running.Lock()
	defer c.running.Unlock()

	if id != 0 {
		if run, found := c.runs[id]; found {
			return run, nil
		}

		return nil, fmt.Errorf("run %d is not in progress; send it as a new task", id)
	}

	switch len(c.runs) {
	case 0:
		return nil, errors.New("no run is in progress; send it as a new task")

	case 1:
		for _, run := range c.runs {
			return run, nil
		}
	}

	return nil, errors.New("several runs are in progress; name the one to tell with run")
}

// slow reports whether a describe or an invoke reaches a provider, and so is
// answered in the background.
func (c *Conn) slow(frame protocol.Frame, handlers Handlers) bool {
	if frame.Method == protocol.MethodDescribe {
		return true
	}

	var params protocol.InvokeParams

	// What cannot be read is refused at once, by answer.
	if json.Unmarshal(frame.Params, &params) != nil {
		return false
	}

	return params.Target != protocol.TargetHost || (handlers.Slow != nil && handlers.Slow(params.Name))
}

// answer serves a describe or an invoke.
func (c *Conn) answer(ctx context.Context, frame protocol.Frame, handlers Handlers) {
	if frame.ID == nil {
		return
	}

	var params protocol.InvokeParams

	if frame.Method == protocol.MethodInvoke && (json.Unmarshal(frame.Params, &params) != nil || params.Name == "") {
		c.fail(frame.ID, protocol.CodeInvalidParams, "invoke needs the name of a function")
		return
	}

	var result any
	var err error

	switch {
	case params.Target == protocol.TargetHost:
		result, err = handlers.Host(ctx, params.Name, params.Params)

	case frame.Method == protocol.MethodDescribe:
		result, err = handlers.Describe(ctx)

	default:
		result, err = handlers.Invoke(ctx, params, talk{conn: c, invoke: *frame.ID})
	}

	// One the front end cancelled says so, in words for the person.
	if err != nil && ctx.Err() != nil {
		err = errors.New("cancelled before it finished")
	}

	if err != nil {
		c.fail(frame.ID, protocol.CodeInternal, err.Error())
		return
	}

	raw, _ := json.Marshal(result)
	_ = c.send(protocol.Frame{ID: frame.ID, Result: raw})
}

// Stop makes Serve return once the request in hand is answered, which
// disconnects the front end.
func (c *Conn) Stop() {
	c.stopped.Store(true)
}

// Shutdown makes Serve return at once, as if the front end had
// disconnected: the runs in progress are cancelled, and Serve returns once
// they end. It is how the host stops when it is asked to by a signal.
func (c *Conn) Shutdown() {
	c.quitOnce.Do(func() { close(c.quit) })
}

// reading reads the front end's frames in the background, so Serve can stop
// without waiting for the next one. It ends at the first error, or once
// Serve has stopped.
func (c *Conn) reading() <-chan read {
	reads := make(chan read)

	go func() {
		for {
			frame, err := c.next()

			select {
			case reads <- read{frame: frame, err: err}:
			case <-c.quit:
				return
			}

			if err != nil {
				return
			}
		}
	}()

	return reads
}

// Log returns a writer whose lines reach the front end as log events. It is
// where the provider's stderr goes, so diagnostics never write over a UI.
// A provider process serves whichever run needs it, so the lines name no
// run.
func (c *Conn) Log() io.Writer {
	return logWriter{conn: c}
}

type logWriter struct {
	conn *Conn
}

func (w logWriter) Write(data []byte) (int, error) {
	if text := string(bytes.TrimRight(data, "\n")); text != "" {
		w.conn.event(protocol.Event{Type: protocol.EventLog, Text: text, Source: protocol.TargetProvider})
	}

	return len(data), nil
}

func (c *Conn) event(event protocol.Event) {
	params, _ := json.Marshal(event)

	// A failed write means the front end is gone; Serve ends on its own.
	_ = c.send(protocol.Frame{Method: protocol.MethodEvent, Params: params})
}

func (c *Conn) fail(id *int64, code int, message string) {
	if id != nil {
		_ = c.send(protocol.Frame{ID: id, Error: &protocol.Error{Code: code, Message: message}})
	}
}

func (c *Conn) send(frame protocol.Frame) error {
	frame.JSONRPC = "2.0"

	c.mu.Lock()
	defer c.mu.Unlock()

	return c.out.Encode(frame)
}

// next returns the next well-formed frame, skipping lines that are not JSON.
func (c *Conn) next() (protocol.Frame, error) {
	for {
		line, err := c.reader.ReadBytes('\n')

		if len(line) > 0 {
			var frame protocol.Frame

			if json.Unmarshal(line, &frame) == nil {
				return frame, nil
			}
		}

		if err != nil {
			return protocol.Frame{}, err
		}
	}
}

// Process is a front end started by the host.
type Process struct {
	cmd *exec.Cmd
	// Reader and Writer carry the protocol.
	Reader io.ReadCloser
	Writer io.WriteCloser
}

// Start runs argv as a front end. It inherits the terminal on stdin, stdout,
// and stderr, and gets the protocol on the file descriptors the protocol
// package names.
func Start(argv []string) (*Process, error) {
	if len(argv) == 0 {
		return nil, errors.New("front end command is empty")
	}

	toFrontend, hostWrites, err := os.Pipe()

	if err != nil {
		return nil, err
	}

	hostReads, fromFrontend, err := os.Pipe()

	if err != nil {
		return nil, err
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// ExtraFiles start at descriptor 3, in order.
	cmd.ExtraFiles = []*os.File{toFrontend, fromFrontend}

	err = cmd.Start()

	// The child holds its own copies of its ends now.
	_ = toFrontend.Close()
	_ = fromFrontend.Close()

	if err != nil {
		_ = hostWrites.Close()
		_ = hostReads.Close()

		return nil, fmt.Errorf("start front end %q: %w", argv[0], err)
	}

	return &Process{cmd: cmd, Reader: hostReads, Writer: hostWrites}, nil
}

// Wait closes the connection and returns the front end's exit code.
func (p *Process) Wait() int {
	_ = p.Writer.Close()
	err := p.cmd.Wait()
	_ = p.Reader.Close()

	var exit *exec.ExitError

	if errors.As(err, &exit) {
		return exit.ExitCode()
	}

	if err != nil {
		return 1
	}

	return 0
}

// Kill stops a front end that did not complete the handshake.
func (p *Process) Kill() {
	_ = p.cmd.Process.Kill()
}
