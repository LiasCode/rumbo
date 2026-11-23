package frontend

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/LiasCode/rumbo/protocol"
)

// Run is one run in progress. It implements agent.Observer, sending what the
// run does as events that name the run and its session, and agent.Inbox,
// with what the front end tells the run.
type Run struct {
	conn *Conn
	// id is the id of the run request, or zero for work that no run request
	// asked for, such as a compaction.
	id     int64
	cancel context.CancelFunc
	// objective is what the run was asked, for the tap.
	objective string

	// mu guards session, steers, and listening. steers are what the front
	// end told the run and the loop has not taken yet; listening is true
	// from when the run is accepted until it ends.
	mu        sync.Mutex
	session   string
	steers    []steer
	listening bool
}

// steer is something the front end told a run, with the images given with
// it, and the request to answer once it is recorded.
type steer struct {
	id     *int64
	text   string
	images []protocol.Image
}

// Report returns a Run for work on session that no run request asked for,
// such as a compaction. Its events name the session and no run, and nothing
// can be told to it.
func (c *Conn) Report(session string) *Run {
	return &Run{conn: c, session: session}
}

// Attach names the session the run works in, for the events from then on. A
// run that starts a session learns it only once the session is made.
func (r *Run) Attach(session string) {
	r.mu.Lock()
	r.session = session
	r.mu.Unlock()

	// Work that no run request asked for has no objective to tell.
	if r.conn.tap != nil && r.id != 0 {
		r.conn.tap(protocol.Event{Type: protocol.EventObjective, Run: r.id, Session: session, Text: r.objective})
	}
}

// Take implements agent.Inbox. Each steer is answered once it is recorded,
// so the front end knows it reached the model.
func (r *Run) Take(record func(text string, images []protocol.Image) error) error {
	r.mu.Lock()
	taken := r.steers
	r.steers = nil
	r.mu.Unlock()

	for index, steer := range taken {
		if err := record(steer.text, steer.images); err != nil {
			// The run fails on this error, so the rest will not be recorded.
			for _, left := range taken[index:] {
				r.conn.fail(left.id, protocol.CodeInternal, err.Error())
			}

			return err
		}

		_ = r.conn.send(protocol.Frame{ID: steer.id, Result: json.RawMessage(`{}`)})
	}

	return nil
}

// hold keeps what the front end told the run until the loop takes it.
func (r *Run) hold(id *int64, text string, images []protocol.Image) {
	r.mu.Lock()
	listening := r.listening

	if listening {
		r.steers = append(r.steers, steer{id: id, text: text, images: images})
	}

	r.mu.Unlock()

	if !listening {
		r.conn.fail(id, protocol.CodeInvalidParams, "the run ended before this reached the model; send it as a new task")
	}
}

// deafen stops holding steers when the run ends. What the run did not take
// will never reach the model, so it fails.
func (r *Run) deafen() {
	r.mu.Lock()
	left := r.steers
	r.steers, r.listening = nil, false
	r.mu.Unlock()

	for _, steer := range left {
		r.conn.fail(steer.id, protocol.CodeInvalidParams, "the run ended before this reached the model; send it as a new task")
	}
}

// Notice implements agent.Observer.
func (r *Run) Notice(text string) {
	r.event(protocol.Event{Type: protocol.EventLog, Text: text, Source: protocol.TargetHost})
}

// ContextUpdated implements agent.Observer.
func (r *Run) ContextUpdated(sections []string) {
	r.event(protocol.Event{Type: protocol.EventContext, Sections: sections})
}

// Text implements agent.Observer.
func (r *Run) Text(delta string) {
	r.event(protocol.Event{Type: protocol.EventText, Text: delta})
}

// ToolStarted implements agent.Observer.
func (r *Run) ToolStarted(name string, args json.RawMessage) {
	r.event(protocol.Event{Type: protocol.EventToolStarted, Name: name, Args: args})
}

// ToolFinished implements agent.Observer.
func (r *Run) ToolFinished(name, result string, failed bool) {
	r.event(protocol.Event{Type: protocol.EventToolFinished, Name: name, Result: result, Failed: failed})
}

// Progress implements tool.Person: what a call says it is doing.
func (r *Run) Progress(tool, text string) {
	r.event(protocol.Event{Type: protocol.EventToolProgress, Name: tool, Text: text})
}

// Ask implements tool.Person: it asks the front end for the person, naming
// the run and the tool. Neither the question nor the answer is recorded.
func (r *Run) Ask(ctx context.Context, tool, question string, secret bool) (string, error) {
	r.mu.Lock()
	session := r.session
	r.mu.Unlock()

	// Work that no run request asked for has nobody following it.
	if r.id == 0 {
		return "", errors.New("there is no one to ask")
	}

	return r.conn.ask(ctx, protocol.FrontendAskParams{Run: r.id, Session: session, Name: tool, Text: question, Secret: secret})
}

// TurnFinished implements agent.Observer.
func (r *Run) TurnFinished(usage protocol.Usage, context, window int64) {
	r.event(protocol.Event{Type: protocol.EventTurn, Usage: &usage, Context: context, Window: window})
}

func (r *Run) event(event protocol.Event) {
	r.mu.Lock()
	event.Run, event.Session = r.id, r.session
	r.mu.Unlock()

	r.conn.event(event)
}
