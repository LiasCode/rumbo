package protocol

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
)

// The Agent Frontend Protocol connects the host to a front end: the program
// a person uses to give objectives and watch the work. It uses the same
// framing as the provider protocol. See docs/frontend-protocol.md.

// Frontend method names. initialize and invoke are shared with the provider
// protocol.
const (
	MethodRun      = "run"
	MethodSteer    = "steer"
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
// session's last run where it stopped. Session names the session of the
// project to run in, and New asks for a new one; with neither, the run goes
// to the current session. A session has one run at a time. Model and
// Options choose, for this run, what the provider described; what is left
// out keeps its stored value.
type RunParams struct {
	Objective string          `json:"objective,omitempty"`
	Resume    bool            `json:"resume,omitempty"`
	Session   string          `json:"session,omitempty"`
	New       bool            `json:"new,omitempty"`
	Model     string          `json:"model,omitempty"`
	Options   json.RawMessage `json:"options,omitempty"`
}

// CancelRunParams names the run to cancel by the id of its run request.
// Without one, every run in progress is cancelled. Invoke names instead a
// function in progress, by the id of its invoke request.
type CancelRunParams struct {
	Run    int64 `json:"run,omitempty"`
	Invoke int64 `json:"invoke,omitempty"`
}

// FrontendAskParams is a question the host asks the front end for the
// person, on behalf of the function called with the invoke whose id is
// Invoke. A Secret answer is not shown while it is typed, and is not
// recorded.
type FrontendAskParams struct {
	Invoke int64  `json:"invoke"`
	Text   string `json:"text"`
	Secret bool   `json:"secret,omitempty"`
}

// SteerParams is what the user says to a run in progress, named by the id of
// its run request; without one, to the only run in progress. It reaches the
// model before its next request, and the run's objective stays as it was.
type SteerParams struct {
	Text string `json:"text"`
	Run  int64  `json:"run,omitempty"`
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

// Event is something that happened during a run. Run is the id of the run
// request it belongs to and Session the session that run works in; both
// are empty for what belongs to no run, such as a provider's diagnostics.
// Invoke is set instead on what a function says while it works: a text
// event naming the invoke request of that function. Type says which other
// fields are set:
//
//   - context: Sections, the names of the ambient sections sent to the model.
//   - text: Text, a piece of the model's visible answer.
//   - tool_started: Name, Args.
//   - tool_finished: Name, Result, Failed.
//   - turn: Usage of one model response; Context, the tokens the
//     conversation now takes; and Window, how many the model holds, when the
//     provider said.
//   - log: Text, and Source saying whose line it is: the provider's own
//     output, or something the host did, such as retrying a request.
type Event struct {
	Type     string          `json:"type"`
	Run      int64           `json:"run,omitempty"`
	Session  string          `json:"session,omitempty"`
	Invoke   int64           `json:"invoke,omitempty"`
	Text     string          `json:"text,omitempty"`
	Sections []string        `json:"sections,omitempty"`
	Name     string          `json:"name,omitempty"`
	Args     json.RawMessage `json:"args,omitempty"`
	Result   string          `json:"result,omitempty"`
	Failed   bool            `json:"failed,omitempty"`
	Source   string          `json:"source,omitempty"`
	Context  int64           `json:"context,omitempty"`
	Window   int64           `json:"window,omitempty"`
	Usage    *Usage          `json:"usage,omitempty"`
}

// FrontendConn is the front end's side of the protocol, for front ends
// written in Go. Its methods may be called from several goroutines at once,
// so several runs can be in progress, and the host can be asked things
// while they work.
type FrontendConn struct {
	done chan struct{}

	mu     sync.Mutex
	out    *json.Encoder
	nextID int64
	// waiting has a channel for each request not yet answered, by id. The
	// answer arrives on it, and for a run so do its events, in order.
	waiting map[int64]chan Frame
	// runs are the ids of the run requests not yet answered, in the order
	// they were sent.
	runs []int64
	// gone is set once the host has closed the connection.
	gone bool
	// talking are the dialogs of the functions called with InvokeWith and
	// not answered yet, by the id of their invoke request.
	talking map[int64]talking
}

// talking is the dialog of one function call, and a context that ends with
// the call, so a question left open is dropped.
type talking struct {
	dialog Dialog
	ctx    context.Context
}

// DialFrontend answers the host's initialize on r and w and returns the
// connection together with what the host said.
func DialFrontend(r io.Reader, w io.Writer, name string) (*FrontendConn, FrontendInitializeParams, error) {
	reader := bufio.NewReader(r)

	var params FrontendInitializeParams

	frame, err := readFrame(reader)

	if err != nil || frame.Method != MethodInitialize || frame.ID == nil {
		return nil, params, errors.New("the host did not send initialize")
	}

	if err := json.Unmarshal(frame.Params, &params); err != nil {
		return nil, params, err
	}

	if params.Protocol != Version {
		return nil, params, fmt.Errorf("host speaks protocol %d, front end speaks %d", params.Protocol, Version)
	}

	conn := &FrontendConn{done: make(chan struct{}), out: json.NewEncoder(w), waiting: map[int64]chan Frame{}, talking: map[int64]talking{}}
	result, _ := json.Marshal(InitializeResult{Protocol: Version, Name: name})

	if err := conn.send(Frame{ID: frame.ID, Result: result}); err != nil {
		return nil, params, err
	}

	go conn.read(reader)

	return conn, params, nil
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
	return c.Launch(params, nil, onEvent)
}

// Launch is Start that first tells onID the id of the run request, which
// names the run to CancelRun and SteerRun before any of its events arrive.
func (c *FrontendConn) Launch(params RunParams, onID func(run int64), onEvent func(Event)) (RunResult, error) {
	var result RunResult

	return result, c.exchange(MethodRun, params, &result, onID, onEvent)
}

// Steer tells the only run in progress text, and returns once it is
// recorded for the model's next request. An error means it never will be:
// no run is in progress, several are, or the run ended first.
func (c *FrontendConn) Steer(text string) error {
	return c.SteerRun(0, text)
}

// SteerRun tells text to one run, named by the id of its request, which its
// events carry, and returns like Steer.
func (c *FrontendConn) SteerRun(run int64, text string) error {
	var result struct{}

	return c.request(MethodSteer, SteerParams{Text: text, Run: run}, &result, nil)
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

// InvokeWith calls a function of the provider like Invoke, and lets it talk
// with the person while it works: what it says goes to dialog.Say, and what
// it asks to dialog.Ask, whose context ends with the call. onID, when set,
// is told the id of the invoke request, which names the call to
// CancelInvoke.
func (c *FrontendConn) InvokeWith(name string, params json.RawMessage, onID func(int64), dialog Dialog) (json.RawMessage, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var call int64

	named := func(id int64) {
		call = id

		c.mu.Lock()
		c.talking[id] = talking{dialog: dialog, ctx: ctx}
		c.mu.Unlock()

		if onID != nil {
			onID(id)
		}
	}

	defer func() {
		c.mu.Lock()
		delete(c.talking, call)
		c.mu.Unlock()
	}()

	said := func(event Event) {
		if event.Type == EventText {
			dialog.Say(event.Text)
		}
	}

	var result json.RawMessage

	return result, c.exchange(MethodInvoke, InvokeParams{Name: name, Params: params}, &result, named, said)
}

// CancelInvoke asks the host to stop a function called with InvokeWith,
// named by the id of its invoke request. The call returns with an error.
func (c *FrontendConn) CancelInvoke(invoke int64) {
	raw, _ := json.Marshal(CancelRunParams{Invoke: invoke})

	_ = c.send(Frame{Method: MethodCancel, Params: raw})
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

// request sends one request and waits for its answer. The events of a run
// are passed to onEvent until the run's answer arrives.
func (c *FrontendConn) request(method string, params, result any, onEvent func(Event)) error {
	return c.exchange(method, params, result, nil, onEvent)
}

// exchange is request that also tells onID the id of the request before it
// is sent.
func (c *FrontendConn) exchange(method string, params, result any, onID func(int64), onEvent func(Event)) error {
	frames := make(chan Frame, 64)

	c.mu.Lock()

	if c.gone {
		c.mu.Unlock()
		return errors.New("the host exited")
	}

	c.nextID++
	id := c.nextID
	c.waiting[id] = frames

	if method == MethodRun {
		c.runs = append(c.runs, id)
	}

	c.mu.Unlock()

	if onID != nil {
		onID(id)
	}

	raw, _ := json.Marshal(params)

	if err := c.send(Frame{ID: &id, Method: method, Params: raw}); err != nil {
		c.forget(id)
		return err
	}

	for frame := range frames {
		if frame.Method == MethodEvent {
			var event Event

			if onEvent != nil && json.Unmarshal(frame.Params, &event) == nil {
				onEvent(event)
			}

			continue
		}

		if frame.Error != nil {
			return frame.Error
		}

		return json.Unmarshal(frame.Result, result)
	}

	return errors.New("the host exited")
}

// forget drops a request that will not be answered.
func (c *FrontendConn) forget(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.runs = slices.DeleteFunc(c.runs, func(run int64) bool { return run == id })
	delete(c.waiting, id)
}

// Cancel asks the host to stop every run in progress. Each still returns,
// with the cancelled status.
func (c *FrontendConn) Cancel() {
	_ = c.send(Frame{Method: MethodCancel})
}

// CancelRun asks the host to stop one run, named by the id of its request,
// which its events carry.
func (c *FrontendConn) CancelRun(run int64) {
	raw, _ := json.Marshal(CancelRunParams{Run: run})

	_ = c.send(Frame{Method: MethodCancel, Params: raw})
}

func (c *FrontendConn) send(frame Frame) error {
	frame.JSONRPC = "2.0"

	c.mu.Lock()
	defer c.mu.Unlock()

	return c.out.Encode(frame)
}

// read passes what the host sends to the request it belongs to: an answer
// to the request with its id, and an event to the run in progress. A run's
// events and its answer share its channel, so the events arrive first.
func (c *FrontendConn) read(reader *bufio.Reader) {
	defer close(c.done)

	for {
		frame, err := readFrame(reader)

		if err != nil {
			break
		}

		// A question the host asks for a function this front end called.
		if frame.Method == MethodAsk && frame.ID != nil {
			c.question(frame)
			continue
		}

		if frames := c.route(frame); frames != nil {
			frames <- frame
		}
	}

	// The host is gone, so nothing waiting will be answered.
	c.mu.Lock()
	defer c.mu.Unlock()

	for id, frames := range c.waiting {
		close(frames)
		delete(c.waiting, id)
	}

	c.runs = nil
	c.gone = true
}

// route returns the channel of the request a frame belongs to, or nil when
// none is waiting for it.
func (c *FrontendConn) route(frame Frame) chan Frame {
	c.mu.Lock()
	defer c.mu.Unlock()

	if frame.Method == MethodEvent {
		var event struct {
			Run    int64 `json:"run"`
			Invoke int64 `json:"invoke"`
		}

		_ = json.Unmarshal(frame.Params, &event)

		// What a function says goes to whoever called it.
		if event.Invoke != 0 {
			return c.waiting[event.Invoke]
		}

		// What names no run, such as a provider's diagnostics, or comes from
		// a host that does not name runs, goes to the run sent first.
		if event.Run == 0 && len(c.runs) > 0 {
			event.Run = c.runs[0]
		}

		if !slices.Contains(c.runs, event.Run) {
			return nil
		}

		return c.waiting[event.Run]
	}

	if frame.Method != "" || frame.ID == nil {
		return nil
	}

	frames := c.waiting[*frame.ID]
	delete(c.waiting, *frame.ID)
	c.runs = slices.DeleteFunc(c.runs, func(run int64) bool { return run == *frame.ID })

	return frames
}

// question answers a question the host asks for a function called with
// InvokeWith, through that call's dialog. The answer waits for the person,
// so it is given in the background.
func (c *FrontendConn) question(frame Frame) {
	var params FrontendAskParams

	_ = json.Unmarshal(frame.Params, &params)

	c.mu.Lock()
	call, found := c.talking[params.Invoke]
	c.mu.Unlock()

	id := *frame.ID

	go func() {
		reply := Frame{ID: &id}

		if !found {
			reply.Error = &Error{Code: CodeInvalidParams, Message: "this front end is not following that function, so it cannot answer for it"}
		} else if answer, err := call.dialog.Ask(call.ctx, params.Text, params.Secret); err != nil {
			reply.Error = &Error{Code: CodeInternal, Message: err.Error()}
		} else {
			reply.Result, _ = json.Marshal(AskResult{Text: answer})
		}

		_ = c.send(reply)
	}()
}

// readFrame returns the next well-formed frame, skipping lines that are not
// JSON.
func readFrame(reader *bufio.Reader) (Frame, error) {
	for {
		line, err := reader.ReadBytes('\n')

		if len(line) > 0 {
			var frame Frame

			if json.Unmarshal(line, &frame) == nil {
				return frame, nil
			}
		}

		if err != nil {
			return Frame{}, err
		}
	}
}
