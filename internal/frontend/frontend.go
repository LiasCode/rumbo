// Package frontend is the host side of the Agent Frontend Protocol. It turns
// a pair of streams into the agent's Observer and into run requests, so the
// loop never knows what kind of front end is attached.
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
	"sync"

	"rumbo/protocol"
)

// Handlers are what the host does for a front end.
type Handlers struct {
	// Run works on one run request and returns the session it ran in.
	Run func(ctx context.Context, params protocol.RunParams) (session string, err error)
	// Describe says what the provider offers and what is in force.
	Describe func(ctx context.Context) (protocol.DescribeResult, error)
	// Invoke calls a function of the provider.
	Invoke func(ctx context.Context, params protocol.InvokeParams) (json.RawMessage, error)
}

// Conn is a connected front end. It implements agent.Observer by sending
// events.
type Conn struct {
	reader *bufio.Reader

	mu  sync.Mutex
	out *json.Encoder
}

// Connect performs the initialize handshake over r and w.
func Connect(r io.Reader, w io.Writer, info protocol.FrontendInitializeParams) (*Conn, error) {
	conn := &Conn{reader: bufio.NewReader(r), out: json.NewEncoder(w)}

	id := int64(1)
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

// Serve handles the front end's requests until it disconnects. A run in
// progress at that point is cancelled and waited for.
func (c *Conn) Serve(handlers Handlers) error {
	var active sync.WaitGroup
	var cancel context.CancelFunc
	var mu sync.Mutex

	stop := func() {
		mu.Lock()
		defer mu.Unlock()

		if cancel != nil {
			cancel()
		}
	}

	defer active.Wait()
	defer stop()

	for {
		frame, err := c.next()

		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return err
		}

		switch frame.Method {
		case protocol.MethodCancel:
			stop()

		case protocol.MethodRun:
			var params protocol.RunParams

			if frame.ID == nil || json.Unmarshal(frame.Params, &params) != nil || (params.Objective == "") != params.Resume {
				c.fail(frame.ID, protocol.CodeInvalidParams, "run needs an id and either an objective or resume")
				continue
			}

			mu.Lock()

			if cancel != nil {
				mu.Unlock()
				c.fail(frame.ID, protocol.CodeInvalidParams, "a run is already in progress")
				continue
			}

			ctx, cancelRun := context.WithCancel(context.Background())
			cancel = cancelRun
			mu.Unlock()

			active.Add(1)

			go func() {
				defer active.Done()

				session, err := handlers.Run(ctx, params)
				result := protocol.RunResult{Status: protocol.RunDone, Session: session}

				switch {
				case ctx.Err() != nil:
					result.Status = protocol.RunCancelled
				case err != nil:
					result.Status = protocol.RunFailed
					result.Error = err.Error()
				}

				mu.Lock()
				cancelRun()
				cancel = nil
				mu.Unlock()

				raw, _ := json.Marshal(result)
				_ = c.send(protocol.Frame{ID: frame.ID, Result: raw})
			}()

		case protocol.MethodDescribe, protocol.MethodInvoke:
			// These go to the provider, which answers one request at a time,
			// so they wait for no run and are refused during one.
			mu.Lock()
			running := cancel != nil
			mu.Unlock()

			if running {
				c.fail(frame.ID, protocol.CodeInvalidParams, "not while a run is in progress")
				continue
			}

			c.answer(frame, handlers)

		default:
			c.fail(frame.ID, protocol.CodeMethodNotFound, "unknown method: "+frame.Method)
		}
	}
}

// answer serves a describe or an invoke.
func (c *Conn) answer(frame protocol.Frame, handlers Handlers) {
	if frame.ID == nil {
		return
	}

	var result any
	var err error

	if frame.Method == protocol.MethodDescribe {
		result, err = handlers.Describe(context.Background())
	} else {
		var params protocol.InvokeParams

		if json.Unmarshal(frame.Params, &params) != nil || params.Name == "" {
			c.fail(frame.ID, protocol.CodeInvalidParams, "invoke needs the name of a function")
			return
		}

		result, err = handlers.Invoke(context.Background(), params)
	}

	if err != nil {
		c.fail(frame.ID, protocol.CodeInternal, err.Error())
		return
	}

	raw, _ := json.Marshal(result)
	_ = c.send(protocol.Frame{ID: frame.ID, Result: raw})
}

// Log returns a writer whose lines reach the front end as log events. It is
// where the provider's stderr goes, so diagnostics never write over a UI.
func (c *Conn) Log() io.Writer {
	return logWriter{conn: c}
}

type logWriter struct {
	conn *Conn
}

func (w logWriter) Write(data []byte) (int, error) {
	if text := string(bytes.TrimRight(data, "\n")); text != "" {
		w.conn.event(protocol.Event{Type: protocol.EventLog, Text: text})
	}

	return len(data), nil
}

// Notice implements agent.Observer.
func (c *Conn) Notice(text string) {
	c.event(protocol.Event{Type: protocol.EventLog, Text: text})
}

// ContextUpdated implements agent.Observer.
func (c *Conn) ContextUpdated(sections []string) {
	c.event(protocol.Event{Type: protocol.EventContext, Sections: sections})
}

// Text implements agent.Observer.
func (c *Conn) Text(delta string) {
	c.event(protocol.Event{Type: protocol.EventText, Text: delta})
}

// ToolStarted implements agent.Observer.
func (c *Conn) ToolStarted(name string, args json.RawMessage) {
	c.event(protocol.Event{Type: protocol.EventToolStarted, Name: name, Args: args})
}

// ToolFinished implements agent.Observer.
func (c *Conn) ToolFinished(name, result string, failed bool) {
	c.event(protocol.Event{Type: protocol.EventToolFinished, Name: name, Result: result, Failed: failed})
}

// TurnFinished implements agent.Observer.
func (c *Conn) TurnFinished(usage protocol.Usage) {
	c.event(protocol.Event{Type: protocol.EventTurn, Usage: &usage})
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
