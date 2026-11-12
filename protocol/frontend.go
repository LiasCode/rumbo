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

// Frontend method names. initialize and invoke are shared with the provider
// protocol.
const (
	MethodRun      = "run"
	MethodEvent    = "event"
	MethodDescribe = "describe"
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
// Functions are what the host itself can be asked through invoke, whatever
// the provider: changing provider or session, compacting, quitting.
type FrontendInitializeParams struct {
	Protocol  int             `json:"protocol"`
	Dir       string          `json:"dir"`
	Session   string          `json:"session,omitempty"`
	Provider  string          `json:"provider"`
	Model     string          `json:"model"`
	Objective string          `json:"objective,omitempty"`
	Resume    bool            `json:"resume,omitempty"`
	Config    json.RawMessage `json:"config,omitempty"`
	Functions []Function      `json:"functions,omitempty"`
}

// HostResult is the answer of a function of the host. Text is the outcome in
// words, for a person; Values is set by a function that lists something.
// State is what the front end is attached to after the call, since a host
// function may have changed it.
type HostResult struct {
	Text   string    `json:"text,omitempty"`
	Values []Value   `json:"values,omitempty"`
	State  HostState `json:"state"`
}

// HostState is what a front end is attached to.
type HostState struct {
	Dir      string `json:"dir"`
	Session  string `json:"session,omitempty"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// RunParams starts a run on Objective, or, with Resume, continues the
// session's last run where it stopped. One run is active at a time. Model
// and Options choose, for this run, what the provider described; what is
// left out keeps its stored value.
type RunParams struct {
	Objective string          `json:"objective,omitempty"`
	Resume    bool            `json:"resume,omitempty"`
	Model     string          `json:"model,omitempty"`
	Options   json.RawMessage `json:"options,omitempty"`
}

// DescribeResult is what the provider in use offers and what is in force.
// The front end can build its choices from it without knowing the provider.
type DescribeResult struct {
	Provider string `json:"provider"`
	// Model and Options are the values a run uses unless it says otherwise.
	Model   string          `json:"model"`
	Options json.RawMessage `json:"options,omitempty"`
	// Offers is what the provider said can be chosen and asked of it.
	Offers Description `json:"offers"`
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
//   - log: Text, and Source saying whose line it is: the provider's own
//     output, or something the host did, such as retrying a request.
type Event struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	Sections []string        `json:"sections,omitempty"`
	Name     string          `json:"name,omitempty"`
	Args     json.RawMessage `json:"args,omitempty"`
	Result   string          `json:"result,omitempty"`
	Failed   bool            `json:"failed,omitempty"`
	Source   string          `json:"source,omitempty"`
	Usage    *Usage          `json:"usage,omitempty"`
}

// FrontendConn is the front end's side of the protocol, for front ends
// written in Go.
type FrontendConn struct {
	frames chan Frame
	// calling lets one request at a time wait for its answer.
	calling sync.Mutex
	done    chan struct{}

	mu     sync.Mutex
	out    *json.Encoder
	nextID int64
}

// DialFrontend answers the host's initialize on r and w and returns the
// connection together with what the host said.
func DialFrontend(r io.Reader, w io.Writer, name string) (*FrontendConn, FrontendInitializeParams, error) {
	conn := &FrontendConn{frames: make(chan Frame, 64), out: json.NewEncoder(w), done: make(chan struct{})}

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
	return c.Start(RunParams{Objective: objective}, onEvent)
}

// Resume asks the host to continue the session's unfinished run, and blocks
// like Run.
func (c *FrontendConn) Resume(onEvent func(Event)) (RunResult, error) {
	return c.Start(RunParams{Resume: true}, onEvent)
}

// Start asks the host for the run the params describe, and blocks like Run.
func (c *FrontendConn) Start(params RunParams, onEvent func(Event)) (RunResult, error) {
	var result RunResult

	return result, c.request(MethodRun, params, &result, onEvent)
}

// Describe asks what the provider in use offers and what is in force.
func (c *FrontendConn) Describe() (DescribeResult, error) {
	var result DescribeResult

	return result, c.request(MethodDescribe, struct{}{}, &result, nil)
}

// Invoke calls a function of the provider and returns its answer as JSON.
func (c *FrontendConn) Invoke(name string, params json.RawMessage) (json.RawMessage, error) {
	var result json.RawMessage

	return result, c.request(MethodInvoke, InvokeParams{Name: name, Params: params}, &result, nil)
}

// InvokeHost calls a function of the host.
func (c *FrontendConn) InvokeHost(name string, params json.RawMessage) (HostResult, error) {
	var result HostResult

	return result, c.request(MethodInvoke, InvokeParams{Name: name, Params: params, Target: TargetHost}, &result, nil)
}

// Done is closed when the host is gone.
func (c *FrontendConn) Done() <-chan struct{} {
	return c.done
}

// request sends one request and waits for its answer, passing the events
// that arrive meanwhile to onEvent.
func (c *FrontendConn) request(method string, params, result any, onEvent func(Event)) error {
	c.calling.Lock()
	defer c.calling.Unlock()

	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()

	raw, _ := json.Marshal(params)

	if err := c.send(Frame{ID: &id, Method: method, Params: raw}); err != nil {
		return err
	}

	for frame := range c.frames {
		if frame.Method == MethodEvent {
			var event Event

			if onEvent != nil && json.Unmarshal(frame.Params, &event) == nil {
				onEvent(event)
			}

			continue
		}

		if frame.ID == nil || *frame.ID != id || frame.Method != "" {
			continue
		}

		if frame.Error != nil {
			return frame.Error
		}

		return json.Unmarshal(frame.Result, result)
	}

	return errors.New("the host exited")
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
	defer close(c.done)
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
