package frontend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/LiasCode/rumbo/protocol"
)

// A front end and the host talk over two pipes, as they do across processes.
func connect(t *testing.T, objective string) (*Conn, *protocol.FrontendConn, protocol.FrontendInitializeParams) {
	t.Helper()

	hostReads, frontendWrites := io.Pipe()
	frontendReads, hostWrites := io.Pipe()

	type dialed struct {
		conn *protocol.FrontendConn
		info protocol.FrontendInitializeParams
		err  error
	}

	ready := make(chan dialed, 1)

	go func() {
		conn, info, err := protocol.DialFrontend(frontendReads, frontendWrites, "test")
		ready <- dialed{conn, info, err}
	}()

	host, err := Connect(hostReads, hostWrites, protocol.FrontendInitializeParams{Model: "m", Objective: objective})

	if err != nil {
		t.Fatal(err)
	}

	front := <-ready

	if front.err != nil {
		t.Fatal(front.err)
	}

	t.Cleanup(func() { frontendWrites.Close() })

	return host, front.conn, front.info
}

// working is the handler of a host whose runs work as their objective says:
// "fail" fails, "hang" waits to be cancelled, "listen" waits until it is
// told something, and "busy" is refused before it starts. Every run works
// in the session named after its objective.
func working(params protocol.RunParams, run *Run) (Work, error) {
	objective := params.Objective

	if params.Resume {
		objective = "the unfinished run"
	}

	if objective == "busy" {
		return nil, errors.New("that session is busy")
	}

	return func(ctx context.Context) (string, error) {
		run.Attach("s-" + objective)

		switch objective {
		case "fail":
			return "s-fail", errors.New("no credit")

		case "hang":
			run.Notice("waiting to be cancelled")
			<-ctx.Done()

			return "s-hang", ctx.Err()

		case "listen":
			run.Notice("waiting to be told")
			heard := hear(ctx, run)
			run.Text("heard " + heard)
			<-ctx.Done()

			return "s-listen", ctx.Err()
		}

		run.Text("working on " + objective)
		run.ToolFinished("bash", "boom", true)

		return "s-" + objective, nil
	}, nil
}

// hear waits until the run is told something, as the loop would between
// turns, and returns it.
func hear(ctx context.Context, run *Run) string {
	var heard []string

	for len(heard) == 0 && ctx.Err() == nil {
		run.Take(func(text string) error {
			heard = append(heard, text)
			return nil
		})

		time.Sleep(time.Millisecond)
	}

	return strings.Join(heard, ", ")
}

// started runs objective in the background, and returns once its first
// event arrived, with that event and a channel that gets the run's result.
func started(t *testing.T, front *protocol.FrontendConn, objective string) (protocol.Event, chan protocol.RunResult) {
	t.Helper()

	first := make(chan protocol.Event, 1)
	result := make(chan protocol.RunResult, 1)

	go func() {
		sent := false

		ended, _ := front.Run(objective, func(event protocol.Event) {
			if !sent {
				first <- event
				sent = true
			}
		})

		result <- ended
	}()

	select {
	case event := <-first:
		return event, result
	case ended := <-result:
		t.Fatalf("%s ended before any event: %+v", objective, ended)
	}

	return protocol.Event{}, result
}

func TestRunStreamsEventsAndReportsOutcome(t *testing.T) {
	host, front, info := connect(t, "from the command line")

	if info.Objective != "from the command line" || info.Model != "m" {
		t.Fatalf("initialize params = %+v", info)
	}

	go host.Serve(Handlers{
		Run: working,
		Describe: func(context.Context) (protocol.DescribeResult, error) {
			return protocol.DescribeResult{
				Provider: "p",
				Model:    "m",
				Offers: protocol.Description{
					Options: []protocol.Option{{Name: "effort", Type: protocol.OptionString, Enum: []string{"low", "high"}}},
				},
			}, nil
		},
		Invoke: func(_ context.Context, params protocol.InvokeParams, _ protocol.Dialog) (json.RawMessage, error) {
			if params.Name != "models" {
				return nil, errors.New("unknown function " + params.Name)
			}

			return json.RawMessage(`{"values":[{"value":"m"},{"value":"n"}]}`), nil
		},
	})

	// The front end learns what can be chosen, and calls a function, without
	// knowing which provider is behind the host.
	described, err := front.Describe()

	if err != nil || described.Provider != "p" || len(described.Offers.Options) != 1 || described.Offers.Options[0].Enum[1] != "high" {
		t.Fatalf("described = %+v, err = %v", described, err)
	}

	raw, err := front.Invoke("models", nil)

	var listed protocol.ValuesResult

	if err != nil || json.Unmarshal(raw, &listed) != nil || len(listed.Values) != 2 {
		t.Fatalf("invoke = %s, err = %v", raw, err)
	}

	if _, err := front.Invoke("nope", nil); err == nil || !strings.Contains(err.Error(), "unknown function nope") {
		t.Fatalf("an unknown function: err = %v", err)
	}

	var events []protocol.Event
	result, err := front.Run("task", func(event protocol.Event) { events = append(events, event) })

	if err != nil || result.Status != protocol.RunDone || result.Session != "s-task" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}

	// Every event names its run and the session it works in.
	if len(events) != 2 || events[0].Text != "working on task" || events[1].Type != protocol.EventToolFinished || !events[1].Failed {
		t.Fatalf("events = %+v", events)
	}

	if events[0].Run == 0 || events[0].Run != events[1].Run || events[0].Session != "s-task" {
		t.Fatalf("events name run %d and session %q", events[0].Run, events[0].Session)
	}

	events = nil

	if _, err := front.Resume(func(event protocol.Event) { events = append(events, event) }); err != nil {
		t.Fatal(err)
	}

	if events[0].Text != "working on the unfinished run" {
		t.Fatalf("resume events = %+v", events)
	}

	result, _ = front.Run("fail", func(protocol.Event) {})

	if result.Status != protocol.RunFailed || result.Error != "no credit" {
		t.Fatalf("failed run = %+v", result)
	}

	// A run the host does not accept is refused before it starts.
	if _, err := front.Run("busy", nil); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("a refused run: err = %v", err)
	}
}

// Several runs work at once. Each gets its own events, and one can be
// cancelled without the others, or all together.
func TestRunsWorkAtOnce(t *testing.T) {
	host, front, _ := connect(t, "")

	go host.Serve(Handlers{
		Run: working,
		Describe: func(context.Context) (protocol.DescribeResult, error) {
			return protocol.DescribeResult{Provider: "p"}, nil
		},
		Host: func(_ context.Context, name string, _ json.RawMessage) (protocol.HostResult, error) {
			return protocol.HostResult{Text: name}, nil
		},
	})

	first, firstEnded := started(t, front, "listen")
	second, secondEnded := started(t, front, "hang")

	if first.Run == second.Run || first.Session != "s-listen" || second.Session != "s-hang" {
		t.Fatalf("first event of each run: %+v and %+v", first, second)
	}

	// The host and the provider still answer while runs work.
	if answer, err := front.InvokeHost("status", nil); err != nil || answer.Text != "status" {
		t.Fatalf("host function during runs = %+v, err = %v", answer, err)
	}

	if described, err := front.Describe(); err != nil || described.Provider != "p" {
		t.Fatalf("describe during runs = %+v, err = %v", described, err)
	}

	front.CancelRun(second.Run)

	if ended := <-secondEnded; ended.Status != protocol.RunCancelled || ended.Session != "s-hang" {
		t.Fatalf("the cancelled run = %+v", ended)
	}

	select {
	case ended := <-firstEnded:
		t.Fatalf("cancelling one run ended the other: %+v", ended)
	default:
	}

	// Steering the only run left needs no run named, and the run hears it.
	if err := front.Steer("use v2"); err != nil {
		t.Fatal(err)
	}

	third, thirdEnded := started(t, front, "hang")

	// A cancel that names no run cancels them all.
	front.Cancel()

	for _, ended := range []chan protocol.RunResult{firstEnded, thirdEnded} {
		if result := <-ended; result.Status != protocol.RunCancelled {
			t.Fatalf("after cancelling all: %+v", result)
		}
	}

	if third.Run == first.Run {
		t.Fatal("two runs share an id")
	}
}

func TestSteerReachesTheRunInProgress(t *testing.T) {
	host, front, _ := connect(t, "")

	go host.Serve(Handlers{Run: working})

	if err := front.Steer("too early"); err == nil || !strings.Contains(err.Error(), "no run is in progress") {
		t.Fatalf("a steer without a run: err = %v", err)
	}

	first, firstEnded := started(t, front, "listen")
	_, secondEnded := started(t, front, "listen")

	// With two runs, a steer has to say which.
	if err := front.Steer("which one?"); err == nil || !strings.Contains(err.Error(), "several runs") {
		t.Fatalf("a steer with two runs: err = %v", err)
	}

	if err := front.SteerRun(first.Run, "use v2"); err != nil {
		t.Fatal(err)
	}

	// What a run never takes fails when the run ends.
	late := make(chan error, 1)

	go func() { late <- front.SteerRun(first.Run, "too late") }()

	run, _ := host.find(first.Run)

	for held := 0; held == 0; time.Sleep(time.Millisecond) {
		run.mu.Lock()
		held = len(run.steers)
		run.mu.Unlock()
	}

	front.CancelRun(first.Run)

	if err := <-late; err == nil || !strings.Contains(err.Error(), "the run ended before") {
		t.Fatalf("a steer the run never took: err = %v", err)
	}

	<-firstEnded

	if err := front.SteerRun(first.Run, "after"); err == nil || !strings.Contains(err.Error(), "not in progress") {
		t.Fatalf("a steer to a run that ended: err = %v", err)
	}

	front.Cancel()
	<-secondEnded
}

// What reaches a provider is answered when it is done, and does not hold up
// the requests that come after it, a cancel among them.
func TestSlowFunctionsDoNotHoldUpTheRest(t *testing.T) {
	host, front, _ := connect(t, "")
	release := make(chan struct{})

	go host.Serve(Handlers{
		Run: working,
		Host: func(_ context.Context, name string, _ json.RawMessage) (protocol.HostResult, error) {
			if name == "compact" {
				<-release
			}

			return protocol.HostResult{Text: name}, nil
		},
		Slow: func(name string) bool { return name == "compact" },
	})

	slow := make(chan string, 1)

	go func() {
		answer, _ := front.InvokeHost("compact", nil)
		slow <- answer.Text
	}()

	_, ended := started(t, front, "hang")

	if answer, err := front.InvokeHost("status", nil); err != nil || answer.Text != "status" {
		t.Fatalf("a quick function behind a slow one = %+v, err = %v", answer, err)
	}

	front.Cancel()

	if result := <-ended; result.Status != protocol.RunCancelled {
		t.Fatalf("a cancel behind a slow function: %+v", result)
	}

	close(release)

	if answer := <-slow; answer != "compact" {
		t.Fatalf("the slow function answered %q", answer)
	}
}

// A front end asks for a session's history, and hears why when there is
// none to tell.
func TestHistoryTellsWhatTheHostHolds(t *testing.T) {
	host, front, _ := connect(t, "")
	asked := make(chan string, 1)

	go host.Serve(Handlers{
		Run: working,
		History: func(params protocol.HistoryParams) (protocol.HistoryResult, error) {
			asked <- params.Session

			if params.Session == "gone" {
				return protocol.HistoryResult{}, errors.New("session gone: not found")
			}

			return protocol.HistoryResult{Session: params.Session, Events: []protocol.Event{{Type: protocol.EventObjective, Text: "earlier"}}}, nil
		},
	})

	told, err := front.History("s-1")

	if err != nil || told.Session != "s-1" || len(told.Events) != 1 || told.Events[0].Text != "earlier" {
		t.Fatalf("history = %+v, err = %v", told, err)
	}

	if session := <-asked; session != "s-1" {
		t.Fatalf("the host was asked about %q", session)
	}

	if _, err := front.History("gone"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("history of a missing session: err = %v", err)
	}
}

// Shutdown ends Serve without waiting for the next request, and cancels the
// runs in progress as a disconnect does.
func TestShutdownEndsServeAsADisconnect(t *testing.T) {
	host, front, _ := connect(t, "")
	served := make(chan error, 1)

	go func() { served <- host.Serve(Handlers{Run: working}) }()

	_, ended := started(t, front, "hang")

	host.Shutdown()

	if result := <-ended; result.Status != protocol.RunCancelled {
		t.Fatalf("the run in progress at shutdown = %+v", result)
	}

	if err := <-served; err != nil {
		t.Fatalf("serve after shutdown: %v", err)
	}
}

// listener is the front end's Dialog in a test: it keeps what it is told,
// and answers with answer, or waits until the call ends when answer is
// empty, telling asked that a question came.
type listener struct {
	said   []string
	secret bool
	answer string
	asked  chan struct{}
}

func (l *listener) Say(text string) {
	l.said = append(l.said, text)
}

func (l *listener) Ask(ctx context.Context, _ string, secret bool) (string, error) {
	l.secret = secret

	if l.asked != nil {
		close(l.asked)
	}

	if l.answer == "" {
		<-ctx.Done()
		return "", ctx.Err()
	}

	return l.answer, nil
}

// What a provider's function says and asks reaches the front end that
// called it, and the answer goes back; a cancel stops a function that
// waits for one.
func TestAFunctionTalksThroughTheFrontEnd(t *testing.T) {
	host, front, _ := connect(t, "")

	go host.Serve(Handlers{
		Run: working,
		Invoke: func(ctx context.Context, params protocol.InvokeParams, dialog protocol.Dialog) (json.RawMessage, error) {
			dialog.Say("open https://example.test/device")

			code, err := dialog.Ask(ctx, "the code the page shows", true)

			if err != nil {
				return nil, err
			}

			return json.Marshal(map[string]string{"code": code})
		},
	})

	someone := &listener{answer: "4711"}
	var called int64

	result, err := front.InvokeWith("login", nil, func(id int64) { called = id }, someone)

	if err != nil || string(result) != `{"code":"4711"}` || called == 0 {
		t.Fatalf("result = %s, err = %v, id %d", result, err, called)
	}

	if len(someone.said) != 1 || someone.said[0] != "open https://example.test/device" || !someone.secret {
		t.Fatalf("said %q, secret %t", someone.said, someone.secret)
	}

	// A function called without a dialog cannot be answered for.
	if _, err := front.Invoke("login", nil); err == nil || !strings.Contains(err.Error(), "cannot answer") {
		t.Fatalf("a question nobody follows: err = %v", err)
	}

	waiting := &listener{asked: make(chan struct{})}
	ended := make(chan error, 1)
	ids := make(chan int64, 1)

	go func() {
		_, err := front.InvokeWith("login", nil, func(id int64) { ids <- id }, waiting)
		ended <- err
	}()

	id := <-ids
	<-waiting.asked
	front.CancelInvoke(id)

	if err := <-ended; err == nil || !strings.Contains(err.Error(), "cancelled before it finished") {
		t.Fatalf("a cancelled function: err = %v", err)
	}
}
