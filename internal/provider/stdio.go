package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/LiasCode/rumbo/protocol"
)

// How long a provider gets to answer after a cancel or a shutdown.
const gracePeriod = 5 * time.Second

// Stdio is a provider process spoken to over its stdin and stdout.
type Stdio struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	frames chan protocol.Frame
	// exited is closed once the provider's stdout has closed.
	exited chan struct{}
	// description is what the provider said about itself on initialize,
	// and config the configuration it said is in force.
	description protocol.Description
	config      json.RawMessage

	mu     sync.Mutex
	nextID int64
	// writing lets an answer to a function's question be written while a
	// call waits for its response.
	writing sync.Mutex
}

// Start runs argv as a provider and performs the initialize handshake. The
// provider's stderr goes to stderr, so its diagnostics reach the user.
// Cancelling ctx only abandons the handshake; the process lives until Close.
func Start(ctx context.Context, argv []string, config json.RawMessage, stderr io.Writer) (*Stdio, error) {
	if len(argv) == 0 {
		return nil, errors.New("provider command is empty")
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stderr = stderr
	// Its own process group keeps the terminal's Ctrl-C away from the
	// provider; the host cancels it through the protocol instead.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()

	if err != nil {
		return nil, err
	}

	stdout, err := cmd.StdoutPipe()

	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start provider %q: %w", argv[0], err)
	}

	stdio := &Stdio{cmd: cmd, stdin: stdin, frames: make(chan protocol.Frame, 64), exited: make(chan struct{})}

	go stdio.read(stdout)

	var result protocol.InitializeResult
	params := protocol.InitializeParams{Protocol: protocol.Version, Config: config}

	if err := stdio.call(ctx, protocol.MethodInitialize, params, &result, nil, nil); err != nil {
		_ = stdio.Close()
		return nil, fmt.Errorf("initialize provider: %w", err)
	}

	if result.Protocol != protocol.Version {
		_ = stdio.Close()
		return nil, fmt.Errorf("provider %q speaks protocol %d, host speaks %d", result.Name, result.Protocol, protocol.Version)
	}

	stdio.description, stdio.config = result.Description, result.Config

	return stdio, nil
}

// Description implements Provider.
func (s *Stdio) Description() protocol.Description {
	return s.description
}

// Config is the configuration the provider said is in force, with its own
// default for every field it was not given. A provider that says nothing
// leaves it empty.
func (s *Stdio) Config() json.RawMessage {
	return s.config
}

// Invoke implements Provider.
func (s *Stdio) Invoke(ctx context.Context, name string, params json.RawMessage, dialog protocol.Dialog) (json.RawMessage, error) {
	var result json.RawMessage
	var onDelta func(string)

	if dialog != nil {
		onDelta = dialog.Say
	}

	err := s.call(ctx, protocol.MethodInvoke, protocol.InvokeParams{Name: name, Params: params}, &result, onDelta, dialog)

	return result, err
}

// Complete implements Provider.
func (s *Stdio) Complete(ctx context.Context, params protocol.CompleteParams, onDelta func(text string)) (protocol.CompleteResult, error) {
	var result protocol.CompleteResult
	err := s.call(ctx, protocol.MethodComplete, params, &result, onDelta, nil)

	return result, err
}

// Exited reports whether the provider process has gone.
func (s *Stdio) Exited() bool {
	select {
	case <-s.exited:
		return true
	default:
		return false
	}
}

// Pid is the system's id of the provider's process.
func (s *Stdio) Pid() int {
	return s.cmd.Process.Pid
}

// Close asks the provider to shut down and kills it if it does not.
func (s *Stdio) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), gracePeriod)
	defer cancel()

	_ = s.call(ctx, protocol.MethodShutdown, struct{}{}, nil, nil, nil)
	_ = s.stdin.Close()

	done := make(chan error, 1)

	go func() {
		done <- s.cmd.Wait()
	}()

	select {
	case <-done:
	case <-time.After(gracePeriod):
		_ = s.cmd.Process.Kill()
		<-done
	}

	return nil
}

// call sends one request and waits for its response. A provider answers one
// request at a time, so calls are serialized; a Pool keeps a process for
// each request that runs at once. What a function asks while the call works
// goes to dialog.
func (s *Stdio) call(ctx context.Context, method string, params, result any, onDelta func(text string), dialog protocol.Dialog) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	id := s.nextID

	// A question still open when the call ends is no longer the person's.
	asking, stopAsking := context.WithCancel(ctx)
	defer stopAsking()

	if err := s.send(protocol.Frame{ID: &id, Method: method}, params); err != nil {
		return err
	}

	done := ctx.Done()
	var deadline <-chan time.Time

	for {
		select {
		case <-done:
			// Ask the provider to stop, then keep reading until it answers.
			_ = s.send(protocol.Frame{Method: protocol.MethodCancel}, protocol.CancelParams{ID: id})
			done = nil
			deadline = time.After(gracePeriod)

		case <-deadline:
			_ = s.cmd.Process.Kill()
			return ctx.Err()

		case frame, open := <-s.frames:
			// A process that died is replaced by the next request, so the
			// same request can succeed when it is sent again.
			if !open {
				return protocol.Transient(errors.New("the provider process exited"))
			}

			if frame.Method == protocol.MethodDelta && onDelta != nil {
				var delta protocol.DeltaParams

				if json.Unmarshal(frame.Params, &delta) == nil && delta.ID == id {
					onDelta(delta.Text)
				}

				continue
			}

			// The answer can take as long as the person does, so it is
			// waited for apart, and a cancel can still be sent meanwhile.
			if frame.Method == protocol.MethodAsk && frame.ID != nil {
				go s.ask(asking, *frame.ID, frame.Params, id, dialog)
				continue
			}

			if frame.ID == nil || *frame.ID != id || frame.Method != "" {
				continue
			}

			if ctx.Err() != nil {
				return ctx.Err()
			}

			if frame.Error != nil {
				return frame.Error
			}

			if result == nil {
				return nil
			}

			return json.Unmarshal(frame.Result, result)
		}
	}
}

// ask passes a question a function asked to the person, through dialog,
// and the answer back. A question that belongs to no function in progress,
// or that nobody can answer, is declined.
func (s *Stdio) ask(ctx context.Context, request int64, raw json.RawMessage, call int64, dialog protocol.Dialog) {
	var params protocol.AskParams

	reply := protocol.Frame{ID: &request}

	switch {
	case json.Unmarshal(raw, &params) != nil:
		reply.Error = &protocol.Error{Code: protocol.CodeInvalidParams, Message: "ask needs the id of its invoke and a text"}

	case dialog == nil || params.ID != call:
		reply.Error = &protocol.Error{Code: protocol.CodeInvalidParams, Message: "there is no one to ask: the question belongs to no function in progress"}

	default:
		if answer, err := dialog.Ask(ctx, params.Text, params.Secret); err != nil {
			reply.Error = &protocol.Error{Code: protocol.CodeInternal, Message: err.Error()}
		} else {
			reply.Result, _ = json.Marshal(protocol.AskResult{Text: answer})
		}
	}

	_ = s.write(reply)
}

func (s *Stdio) send(frame protocol.Frame, params any) error {
	raw, err := json.Marshal(params)

	if err != nil {
		return err
	}

	frame.Params = raw

	return s.write(frame)
}

func (s *Stdio) write(frame protocol.Frame) error {
	frame.JSONRPC = "2.0"

	line, err := json.Marshal(frame)

	if err != nil {
		return err
	}

	s.writing.Lock()
	defer s.writing.Unlock()

	if _, err := s.stdin.Write(append(line, '\n')); err != nil {
		return protocol.Transient(fmt.Errorf("the provider process could not be reached: %w", err))
	}

	return nil
}

// read forwards the provider's frames until its stdout closes. Lines that are
// not JSON are skipped so a stray print does not break the session.
func (s *Stdio) read(stdout io.Reader) {
	// Exited is true by the time a request sees the frames end, so the
	// process is not given back to a pool.
	defer close(s.frames)
	defer close(s.exited)

	reader := bufio.NewReader(stdout)

	for {
		line, err := reader.ReadBytes('\n')

		if len(line) > 0 {
			var frame protocol.Frame

			if json.Unmarshal(line, &frame) == nil {
				s.frames <- frame
			}
		}

		if err != nil {
			return
		}
	}
}
