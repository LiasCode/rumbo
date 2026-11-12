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

	"rumbo/protocol"
)

// How long a provider gets to answer after a cancel or a shutdown.
const gracePeriod = 5 * time.Second

// Stdio is a provider process spoken to over its stdin and stdout.
type Stdio struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	frames chan protocol.Frame
	// description is what the provider said about itself on initialize.
	description protocol.Description

	mu     sync.Mutex
	nextID int64
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

	stdio := &Stdio{cmd: cmd, stdin: stdin, frames: make(chan protocol.Frame, 64)}

	go stdio.read(stdout)

	var result protocol.InitializeResult
	params := protocol.InitializeParams{Protocol: protocol.Version, Config: config}

	if err := stdio.call(ctx, protocol.MethodInitialize, params, &result, nil); err != nil {
		_ = stdio.Close()
		return nil, fmt.Errorf("initialize provider: %w", err)
	}

	if result.Protocol != protocol.Version {
		_ = stdio.Close()
		return nil, fmt.Errorf("provider %q speaks protocol %d, host speaks %d", result.Name, result.Protocol, protocol.Version)
	}

	stdio.description = result.Description

	return stdio, nil
}

// Description implements Provider.
func (s *Stdio) Description() protocol.Description {
	return s.description
}

// Invoke implements Provider.
func (s *Stdio) Invoke(ctx context.Context, name string, params json.RawMessage) (json.RawMessage, error) {
	var result json.RawMessage
	err := s.call(ctx, protocol.MethodInvoke, protocol.InvokeParams{Name: name, Params: params}, &result, nil)

	return result, err
}

// Complete implements Provider.
func (s *Stdio) Complete(ctx context.Context, params protocol.CompleteParams, onDelta func(text string)) (protocol.CompleteResult, error) {
	var result protocol.CompleteResult
	err := s.call(ctx, protocol.MethodComplete, params, &result, onDelta)

	return result, err
}

// Close asks the provider to shut down and kills it if it does not.
func (s *Stdio) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), gracePeriod)
	defer cancel()

	_ = s.call(ctx, protocol.MethodShutdown, struct{}{}, nil, nil)
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

// call sends one request and waits for its response. The agent runs one
// request at a time, so calls are serialized.
func (s *Stdio) call(ctx context.Context, method string, params, result any, onDelta func(text string)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	id := s.nextID

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
			if !open {
				return errors.New("provider exited")
			}

			if frame.Method == protocol.MethodDelta && onDelta != nil {
				var delta protocol.DeltaParams

				if json.Unmarshal(frame.Params, &delta) == nil && delta.ID == id {
					onDelta(delta.Text)
				}

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

func (s *Stdio) send(frame protocol.Frame, params any) error {
	raw, err := json.Marshal(params)

	if err != nil {
		return err
	}

	frame.JSONRPC = "2.0"
	frame.Params = raw

	line, err := json.Marshal(frame)

	if err != nil {
		return err
	}

	_, err = s.stdin.Write(append(line, '\n'))

	return err
}

// read forwards the provider's frames until its stdout closes. Lines that are
// not JSON are skipped so a stray print does not break the session.
func (s *Stdio) read(stdout io.Reader) {
	defer close(s.frames)

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
