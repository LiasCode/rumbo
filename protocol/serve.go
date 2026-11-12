package protocol

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

// Invoker is what a provider that describes functions also implements. The
// result is sent to the caller as JSON.
type Invoker interface {
	Invoke(ctx context.Context, name string, params json.RawMessage) (any, error)
}

// Handler is what a provider implements. Serve turns it into a protocol
// server, so a provider written in Go needs nothing else.
type Handler interface {
	Initialize(ctx context.Context, params InitializeParams) (InitializeResult, error)
	Complete(ctx context.Context, params CompleteParams, emit func(text string)) (CompleteResult, error)
}

// Serve speaks the protocol on r and w until the host sends shutdown or
// closes the stream.
func Serve(ctx context.Context, r io.Reader, w io.Writer, handler Handler) error {
	server := &server{handler: handler, out: json.NewEncoder(w), running: map[int64]context.CancelFunc{}}
	reader := bufio.NewReader(r)

	for {
		line, err := reader.ReadBytes('\n')

		if len(line) > 0 {
			var frame Frame

			if json.Unmarshal(line, &frame) == nil && frame.Method != "" {
				if server.dispatch(ctx, frame) {
					server.wait.Wait()
					return nil
				}
			}
		}

		if err != nil {
			server.wait.Wait()

			if errors.Is(err, io.EOF) {
				return nil
			}

			return err
		}
	}
}

type server struct {
	handler Handler
	wait    sync.WaitGroup

	mu      sync.Mutex
	out     *json.Encoder
	running map[int64]context.CancelFunc
}

// dispatch handles one incoming frame and reports whether to shut down.
func (s *server) dispatch(ctx context.Context, frame Frame) bool {
	switch frame.Method {
	case MethodInitialize:
		var params InitializeParams

		if !s.decode(frame, &params) {
			return false
		}

		result, err := s.handler.Initialize(ctx, params)
		s.reply(frame.ID, result, err)

	case MethodInvoke:
		var params InvokeParams

		if !s.decode(frame, &params) {
			return false
		}

		invoker, able := s.handler.(Invoker)

		if !able {
			s.reply(frame.ID, nil, &Error{Code: CodeMethodNotFound, Message: "this provider has no functions"})
			return false
		}

		result, err := invoker.Invoke(ctx, params.Name, params.Params)
		s.reply(frame.ID, result, err)

	case MethodComplete:
		var params CompleteParams

		if !s.decode(frame, &params) || frame.ID == nil {
			return false
		}

		s.complete(ctx, *frame.ID, params)

	case MethodCancel:
		var params CancelParams

		if json.Unmarshal(frame.Params, &params) == nil {
			s.mu.Lock()
			cancel := s.running[params.ID]
			s.mu.Unlock()

			if cancel != nil {
				cancel()
			}
		}

	case MethodShutdown:
		s.reply(frame.ID, struct{}{}, nil)
		return true

	default:
		if frame.ID != nil {
			s.send(Frame{ID: frame.ID, Error: &Error{Code: CodeMethodNotFound, Message: "unknown method: " + frame.Method}})
		}
	}

	return false
}

// complete runs a completion in the background so a cancel can still arrive.
func (s *server) complete(ctx context.Context, id int64, params CompleteParams) {
	runCtx, cancel := context.WithCancel(ctx)

	s.mu.Lock()
	s.running[id] = cancel
	s.mu.Unlock()

	s.wait.Go(func() {
		defer cancel()

		emit := func(text string) {
			raw, _ := json.Marshal(DeltaParams{ID: id, Text: text})
			s.send(Frame{Method: MethodDelta, Params: raw})
		}

		result, err := s.handler.Complete(runCtx, params, emit)

		s.mu.Lock()
		delete(s.running, id)
		s.mu.Unlock()

		s.reply(&id, result, err)
	})
}

func (s *server) decode(frame Frame, params any) bool {
	if err := json.Unmarshal(frame.Params, params); err != nil {
		if frame.ID != nil {
			s.send(Frame{ID: frame.ID, Error: &Error{Code: CodeInvalidParams, Message: err.Error()}})
		}

		return false
	}

	return true
}

func (s *server) reply(id *int64, result any, err error) {
	if id == nil {
		return
	}

	// A handler picks the code by returning an *Error, as Transient does.

	if failure, ok := errors.AsType[*Error](err); ok {
		s.send(Frame{ID: id, Error: failure})
		return
	}

	if err != nil {
		s.send(Frame{ID: id, Error: &Error{Code: CodeInternal, Message: err.Error()}})
		return
	}

	raw, err := json.Marshal(result)

	if err != nil {
		s.send(Frame{ID: id, Error: &Error{Code: CodeInternal, Message: err.Error()}})
		return
	}

	s.send(Frame{ID: id, Result: raw})
}

func (s *server) send(frame Frame) {
	frame.JSONRPC = "2.0"

	s.mu.Lock()
	defer s.mu.Unlock()

	// A failed write means the host is gone; the read loop ends on its own.
	_ = s.out.Encode(frame)
}
