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

// FrontendVersion is the version of the front end protocol this package
// implements. In version 2 a host is one session: a run names no session
// and starts no other one, and history is the host's session's.
const FrontendVersion = 2

// Frontend method names. initialize and invoke are shared with the provider
// protocol.
const (
	MethodRun       = "run"
	MethodSteer     = "steer"
	MethodEvent     = "event"
	MethodDescribe  = "describe"
	MethodHistory   = "history"
	MethodRecall    = "recall"
	MethodProcesses = "processes"
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
	// What a tool says it is doing while it works, when it says.
	EventToolProgress = "tool_progress"
	EventTurn         = "turn"
	EventLog          = "log"
	// What only a session's history holds: what the person asked, and how
	// each run ended. A front end knows them of the runs it follows.
	EventObjective = "objective"
	EventSteer     = "steer"
	EventEnded     = "ended"
)

// What a function of the host acts on: rumbo itself, the same for every
// session and provider; the host's session; the registered providers; or
// the registered extensions.
const (
	GroupSystem    = "system"
	GroupSession   = "session"
	GroupProvider  = "provider"
	GroupExtension = "extension"
)

// Run outcomes.
const (
	RunDone      = "done"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
)

// What a process of a host is: the host itself; the front end, when the
// host started it; a provider or an extension, named; what the model left
// running in the background; or a command the host runs for the moment,
// such as one of the bash tool.
const (
	ProcessHost       = "host"
	ProcessFrontend   = "frontend"
	ProcessProvider   = "provider"
	ProcessExtension  = "extension"
	ProcessBackground = "background"
	ProcessCommand    = "command"
)

// FrontendInitializeParams tells the front end what it is attached to.
// Session is empty until the first run of a new session, and Title is what
// the session is called. Objective is what the user already passed on the
// command line, if anything; the front end is expected to run it. Resume
// means the user asked to continue the session's unfinished run instead;
// the front end is expected to send a resume.
// Functions are what the host itself can be asked through invoke, whatever
// the provider: changing provider, compacting, quitting.
type FrontendInitializeParams struct {
	Protocol  int             `json:"protocol"`
	Dir       string          `json:"dir"`
	Session   string          `json:"session,omitempty"`
	Title     string          `json:"title,omitempty"`
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

// HostState is what a front end is attached to: the directory the session
// works in, which is its root, the session and what it is called, both
// empty until its first run makes it, and what it uses.
type HostState struct {
	Dir      string `json:"dir"`
	Session  string `json:"session,omitempty"`
	Title    string `json:"title,omitempty"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// RunParams starts a run on Objective, and the Images the person gave with
// it, or, with Resume, continues the session's last run where it stopped.
// The run works in the host's session, which has one run at a time. Model
// and Options choose, for this run, what the provider described; what is
// left out keeps its stored value.
type RunParams struct {
	Objective string          `json:"objective,omitempty"`
	Images    []Image         `json:"images,omitempty"`
	Resume    bool            `json:"resume,omitempty"`
	Model     string          `json:"model,omitempty"`
	Options   json.RawMessage `json:"options,omitempty"`
}

// CancelRunParams names the run to cancel by the id of its run request.
// Without one, the run in progress is cancelled. Invoke names instead a
// function in progress, by the id of its invoke request.
type CancelRunParams struct {
	Run    int64 `json:"run,omitempty"`
	Invoke int64 `json:"invoke,omitempty"`
}

// FrontendAskParams is a question the host asks the front end for the
// person, on behalf of the function called with the invoke whose id is
// Invoke; or, when Invoke is zero, of the tool called Name that the model
// called in the run with the id Run, in Session. A Secret answer is not
// shown while it is typed, and is not recorded.
type FrontendAskParams struct {
	Invoke  int64  `json:"invoke,omitempty"`
	Run     int64  `json:"run,omitempty"`
	Session string `json:"session,omitempty"`
	Name    string `json:"name,omitempty"`
	Text    string `json:"text"`
	Secret  bool   `json:"secret,omitempty"`
}

// SteerParams is what the user says to a run in progress, named by the id of
// its run request; without one, to the run in progress. It reaches the
// model before its next request, and the run's objective stays as it was.
type SteerParams struct {
	Text   string  `json:"text"`
	Images []Image `json:"images,omitempty"`
	Run    int64   `json:"run,omitempty"`
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
	// Extensions are the extensions that have functions, by name, which an
	// invoke reaches with the extension target.
	Extensions []ExtensionOffer `json:"extensions,omitempty"`
}

// ExtensionOffer is what an extension offers the person: its functions.
type ExtensionOffer struct {
	Name      string     `json:"name"`
	Functions []Function `json:"functions"`
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
//   - tool_progress: Name, and Text, what the call says it is doing.
//   - turn: Usage of one model response; Context, the tokens the
//     conversation now takes; and Window, how many the model holds, when the
//     provider said.
//   - log: Text, and Source saying whose line it is: the provider's own
//     output, an extension's, which starts with its name, or something the
//     host did, such as retrying a request.
//   - objective: Text, the objective a run was given, and the Images given
//     with it.
//   - steer: Text, what the person told a run while it worked, and Images.
//   - ended: Status, how a run ended, as a RunResult says it, and Text,
//     why a failed run failed.
//
// The last three appear in a session's history, which is told in the same
// events as the runs that made it.
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
	Images   []Image         `json:"images,omitempty"`
	Status   string          `json:"status,omitempty"`
}

// HistoryResult is what the host's session holds, told in events, oldest
// first, so a front end can show a session it opens as if it had followed
// every run. Session is empty when the session is new and holds nothing.
type HistoryResult struct {
	Session string  `json:"session,omitempty"`
	Events  []Event `json:"events"`
}

// RecallResult is what the person asked in the sessions started in the
// host's directory, most recent first and each once: the objectives, and
// what was said to runs while they worked. A front end brings it back to
// its input.
type RecallResult struct {
	Asked []string `json:"asked"`
}

// ProcessUsage is one process of a host and what it uses of the machine.
// Role says what it is, and Name which provider or extension. A process
// one of those started has its role and name, and Parent says which
// started it. Memory is what it holds in memory now, in bytes, and CPU the
// processor time it has used since it started, in seconds, both as the
// system reports them.
type ProcessUsage struct {
	PID     int     `json:"pid"`
	Parent  int     `json:"parent"`
	Role    string  `json:"role"`
	Name    string  `json:"name,omitempty"`
	Command string  `json:"command"`
	Memory  int64   `json:"memory"`
	CPU     float64 `json:"cpu"`
}

// ProcessesResult is the processes of a host, itself first and each of the
// others after the one that started it.
type ProcessesResult struct {
	Processes []ProcessUsage `json:"processes"`
}

// FrontendConn is the front end's side of the protocol, for front ends
// written in Go. Its methods may be called from several goroutines at once,
// so the host can be asked things while a run works.
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
	// asker answers what a tool asks the person during a run, and asking
	// are the ends of the questions still open, by the id of their run.
	asker  func(ctx context.Context, question FrontendAskParams) (string, error)
	asking map[int64][]context.CancelFunc
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
	return DialFrontendConfigured(r, w, name, nil)
}

// DialFrontendConfigured is DialFrontend for a front end with a start
// configuration: inForce is given the one registered, and returns the one in
// force, with the front end's own default for every field it left out.
func DialFrontendConfigured(r io.Reader, w io.Writer, name string, inForce func(given json.RawMessage) json.RawMessage) (*FrontendConn, FrontendInitializeParams, error) {
	reader := bufio.NewReader(r)

	var params FrontendInitializeParams

	frame, err := readFrame(reader)

	if err != nil || frame.Method != MethodInitialize || frame.ID == nil {
		return nil, params, errors.New("the host did not send initialize")
	}

	if err := json.Unmarshal(frame.Params, &params); err != nil {
		return nil, params, err
	}

	if params.Protocol != FrontendVersion {
		return nil, params, fmt.Errorf("host speaks protocol %d, front end speaks %d", params.Protocol, FrontendVersion)
	}

	conn := &FrontendConn{done: make(chan struct{}), out: json.NewEncoder(w), waiting: map[int64]chan Frame{}, talking: map[int64]talking{}, asking: map[int64][]context.CancelFunc{}}
	answer := InitializeResult{Protocol: FrontendVersion, Name: name}

	if inForce != nil {
		answer.Config = inForce(params.Config)
	}

	result, _ := json.Marshal(answer)

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

// Steer tells the run in progress text, and returns once it is recorded
// for the model's next request. An error means it never will be: no run is
// in progress, or the run ended first.
func (c *FrontendConn) Steer(text string) error {
	return c.SteerRun(0, text)
}

// SteerRun tells text, and images, to one run, named by the id of its
// request, which its events carry, and returns like Steer.
func (c *FrontendConn) SteerRun(run int64, text string, images ...Image) error {
	var result struct{}

	return c.request(MethodSteer, SteerParams{Text: text, Images: images, Run: run}, &result, nil)
}

// Describe asks what the provider in use offers and what is in force.
func (c *FrontendConn) Describe() (DescribeResult, error) {
	var result DescribeResult

	return result, c.request(MethodDescribe, struct{}{}, &result, nil)
}

// History returns what the host's session holds, told in events.
func (c *FrontendConn) History() (HistoryResult, error) {
	var result HistoryResult

	return result, c.request(MethodHistory, struct{}{}, &result, nil)
}

// Recall returns what the person asked in the sessions started in the
// host's directory, most recent first.
func (c *FrontendConn) Recall() (RecallResult, error) {
	var result RecallResult

	return result, c.request(MethodRecall, struct{}{}, &result, nil)
}

// Processes returns the processes of the host and what each uses of the
// machine, so a front end that holds several hosts can say what they cost,
// and close those it does not need.
func (c *FrontendConn) Processes() (ProcessesResult, error) {
	var result ProcessesResult

	return result, c.request(MethodProcesses, struct{}{}, &result, nil)
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
	return c.invokeWith(InvokeParams{Name: name, Params: params}, onID, dialog)
}

// InvokeExtension calls a function of the extension called extension, as
// InvokeWith calls one of the provider. A nil dialog lets it say and ask
// nothing.
func (c *FrontendConn) InvokeExtension(extension, name string, params json.RawMessage, onID func(int64), dialog Dialog) (json.RawMessage, error) {
	asked := InvokeParams{Name: name, Params: params, Target: TargetExtension, Extension: extension}

	if dialog == nil {
		var result json.RawMessage

		return result, c.exchange(MethodInvoke, asked, &result, onID, nil)
	}

	return c.invokeWith(asked, onID, dialog)
}

// AnswerRuns has asked answer what a tool asks the person during a run:
// the question names the run, its session, and the tool, and the context
// ends when the run does. Without it such a question is declined, which is
// right for a front end nobody is at. It is set before any run is started.
func (c *FrontendConn) AnswerRuns(asked func(ctx context.Context, question FrontendAskParams) (string, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.asker = asked
}

func (c *FrontendConn) invokeWith(asked InvokeParams, onID func(int64), dialog Dialog) (json.RawMessage, error) {
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

	return result, c.exchange(MethodInvoke, asked, &result, named, said)
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

// Cancel asks the host to stop the run in progress, which still returns,
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

	for run := range c.asking {
		c.unask(run)
	}

	c.runs = nil
	c.gone = true
}

// unask ends the questions still open of a run that is over. The caller
// holds the lock.
func (c *FrontendConn) unask(run int64) {
	for _, end := range c.asking[run] {
		end()
	}

	delete(c.asking, run)
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
	c.unask(*frame.ID)

	return frames
}

// question answers a question the host asks: for a function called with
// InvokeWith, through that call's dialog, or for a tool of a run, through
// what AnswerRuns was given. The answer waits for the person, so it is
// given in the background.
func (c *FrontendConn) question(frame Frame) {
	var params FrontendAskParams

	_ = json.Unmarshal(frame.Params, &params)

	id := *frame.ID

	if params.Invoke == 0 {
		c.runQuestion(id, params)
		return
	}

	c.mu.Lock()
	call, found := c.talking[params.Invoke]
	c.mu.Unlock()

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

// runQuestion answers what a tool asks during a run, until the run ends.
func (c *FrontendConn) runQuestion(id int64, params FrontendAskParams) {
	c.mu.Lock()
	asker := c.asker
	following := slices.Contains(c.runs, params.Run)

	var ctx context.Context

	if asker != nil && following {
		var end context.CancelFunc

		ctx, end = context.WithCancel(context.Background())
		c.asking[params.Run] = append(c.asking[params.Run], end)
	}

	c.mu.Unlock()

	go func() {
		reply := Frame{ID: &id}

		if ctx == nil {
			reply.Error = &Error{Code: CodeInvalidParams, Message: "nobody is here to answer for that run"}
		} else if answer, err := asker(ctx, params); err != nil {
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
