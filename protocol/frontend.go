package protocol

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// The Agent Frontend Protocol connects the host to a front end: the program
// a person uses to give objectives and watch the work. It uses the same
// framing as the provider protocol. See docs/frontend-protocol.md.

// Frontend method names. initialize is shared with the provider protocol.
const (
	MethodRun   = "run"
	MethodEvent = "event"
)

// When the host starts a front end, the protocol is on these file
// descriptors, and stdin, stdout, and stderr stay connected to the terminal.
const (
	FrontendReadFD  = 3
	FrontendWriteFD = 4
)

// Event types.
const (
	EventContext      = "context"
	EventText         = "text"
	EventToolStarted  = "tool_started"
	EventToolFinished = "tool_finished"
	EventTurn         = "turn"
	EventLog          = "log"
)

// Run outcomes.
const (
	RunDone      = "done"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
)

// FrontendInitializeParams tells the front end what it is attached to.
// Session is empty until the first run of a new session. Objective is what
// the user already passed on the command line, if anything; the front end is
// expected to run it. Resume means the user asked to continue the session's
// unfinished run instead; the front end is expected to send a resume.
type FrontendInitializeParams struct {
	Protocol  int             `json:"protocol"`
	Dir       string          `json:"dir"`
	Session   string          `json:"session,omitempty"`
	Provider  string          `json:"provider"`
	Model     string          `json:"model"`
	Objective string          `json:"objective,omitempty"`
	Resume    bool            `json:"resume,omitempty"`
	Config    json.RawMessage `json:"config,omitempty"`
}

// RunParams starts a run on Objective, or, with Resume, continues the
// session's last run where it stopped. One run is active at a time.
type RunParams struct {
	Objective string `json:"objective,omitempty"`
	Resume    bool   `json:"resume,omitempty"`
}

// RunResult is sent when the run ends, however it ends. Error explains a
// failed run.
type RunResult struct {
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
	Session string `json:"session"`
}

// Event is something that happened during a run. Type says which fields are
// set:
//
//   - context: Sections, the names of the ambient sections sent to the model.
//   - text: Text, a piece of the model's visible answer.
//   - tool_started: Name, Args.
//   - tool_finished: Name, Result, Failed.
//   - turn: Usage of one model response.
//   - log: Text, a diagnostic line from the host or the provider.
type Event struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	Sections []string        `json:"sections,omitempty"`
	Name     string          `json:"name,omitempty"`
	Args     json.RawMessage `json:"args,omitempty"`
	Result   string          `json:"result,omitempty"`
	Failed   bool            `json:"failed,omitempty"`
	Usage    *Usage          `json:"usage,omitempty"`
}

// FrontendConn is the front end's side of the protocol, for front ends
// written in Go.
type FrontendConn struct {
	frames chan Frame

	mu     sync.Mutex
	out    *json.Encoder
	nextID int64
}

// DialFrontend answers the host's initialize on r and w and returns the
// connection together with what the host said.
func DialFrontend(r io.Reader, w io.Writer, name string) (*FrontendConn, FrontendInitializeParams, error) {
	conn := &FrontendConn{frames: make(chan Frame, 64), out: json.NewEncoder(w)}

	go conn.read(r)

	var params FrontendInitializeParams

	frame, open := <-conn.frames

	if !open || frame.Method != MethodInitialize || frame.ID == nil {
		return nil, params, errors.New("the host did not send initialize")
	}

	if err := json.Unmarshal(frame.Params, &params); err != nil {
		return nil, params, err
	}

	if params.Protocol != Version {
		return nil, params, fmt.Errorf("host speaks protocol %d, front end speaks %d", params.Protocol, Version)
	}

	result, _ := json.Marshal(InitializeResult{Protocol: Version, Name: name})

	return conn, params, conn.send(Frame{ID: frame.ID, Result: result})
}

// Run asks the host to work on objective and blocks until the run ends,
// passing every event to onEvent.
func (c *FrontendConn) Run(objective string, onEvent func(Event)) (RunResult, error) {
	return c.run(RunParams{Objective: objective}, onEvent)
}

// Resume asks the host to continue the session's unfinished run, and blocks
// like Run.
func (c *FrontendConn) Resume(onEvent func(Event)) (RunResult, error) {
	return c.run(RunParams{Resume: true}, onEvent)
}

func (c *FrontendConn) run(request RunParams, onEvent func(Event)) (RunResult, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()

	var result RunResult

	params, _ := json.Marshal(request)

	if err := c.send(Frame{ID: &id, Method: MethodRun, Params: params}); err != nil {
		return result, err
	}

	for frame := range c.frames {
		if frame.Method == MethodEvent {
			var event Event

			if json.Unmarshal(frame.Params, &event) == nil {
				onEvent(event)
			}

			continue
		}

		if frame.ID == nil || *frame.ID != id || frame.Method != "" {
			continue
		}

		if frame.Error != nil {
			return result, frame.Error
		}

		return result, json.Unmarshal(frame.Result, &result)
	}

	return result, errors.New("the host exited")
}

// Cancel asks the host to stop the current run. Run still returns, with the
// cancelled status.
func (c *FrontendConn) Cancel() {
	_ = c.send(Frame{Method: MethodCancel})
}

func (c *FrontendConn) send(frame Frame) error {
	frame.JSONRPC = "2.0"

	c.mu.Lock()
	defer c.mu.Unlock()

	return c.out.Encode(frame)
}

func (c *FrontendConn) read(r io.Reader) {
	defer close(c.frames)

	reader := bufio.NewReader(r)

	for {
		line, err := reader.ReadBytes('\n')

		if len(line) > 0 {
			var frame Frame

			if json.Unmarshal(line, &frame) == nil {
				c.frames <- frame
			}
		}

		if err != nil {
			return
		}
	}
}
