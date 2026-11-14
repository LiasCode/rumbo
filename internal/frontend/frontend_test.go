package frontend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"rumbo/protocol"
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

func TestRunStreamsEventsAndReportsOutcome(t *testing.T) {
	host, front, info := connect(t, "from the command line")

	if info.Objective != "from the command line" || info.Model != "m" {
		t.Fatalf("initialize params = %+v", info)
	}

	served := make(chan error, 1)

	go func() {
		served <- host.Serve(Handlers{Run: func(ctx context.Context, params protocol.RunParams) (string, error) {
			objective := params.Objective

			if params.Resume {
				objective = "the unfinished run"
			}

			switch objective {
			case "fail":
				return "s1", errors.New("no credit")
			case "hang":
				<-ctx.Done()
				return "s1", ctx.Err()
			}

			host.Text("working on " + objective)
			host.ToolFinished("bash", "boom", true)

			return "s1", nil
		},
			Describe: func(context.Context) (protocol.DescribeResult, error) {
				return protocol.DescribeResult{
					Provider: "p",
					Model:    "m",
					Offers: protocol.Description{
						Options: []protocol.Option{{Name: "effort", Type: protocol.OptionString, Enum: []string{"low", "high"}}},
					},
				}, nil
			},
			Invoke: func(_ context.Context, params protocol.InvokeParams) (json.RawMessage, error) {
				if params.Name != "models" {
					return nil, errors.New("unknown function " + params.Name)
				}

				return json.RawMessage(`{"values":[{"value":"m"},{"value":"n"}]}`), nil
			},
		})
	}()

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

	if err != nil || result.Status != protocol.RunDone || result.Session != "s1" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}

	if len(events) != 2 || events[0].Text != "working on task" || events[1].Type != protocol.EventToolFinished || !events[1].Failed {
		t.Fatalf("events = %+v", events)
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

	// Cancel ends a run that would otherwise never return.
	hung := make(chan protocol.RunResult, 1)

	go func() {
		result, _ := front.Run("hang", func(protocol.Event) {})
		hung <- result
	}()

	// Cancel may arrive before the run starts; repeat until the run reports.
	var cancelled protocol.RunResult

	for done := false; !done; {
		select {
		case cancelled = <-hung:
			done = true
		default:
			front.Cancel()
		}
	}

	if cancelled.Status != protocol.RunCancelled {
		t.Fatalf("cancelled run = %+v", cancelled)
	}
}

func TestHostAnswersDuringARun(t *testing.T) {
	host, front, _ := connect(t, "")
	release := make(chan struct{})

	go host.Serve(Handlers{
		Run: func(ctx context.Context, params protocol.RunParams) (string, error) {
			host.Text("started")

			select {
			case <-release:
			case <-ctx.Done():
			}

			host.Text("finished")

			return "s1", nil
		},
		Describe: func(context.Context) (protocol.DescribeResult, error) {
			return protocol.DescribeResult{Provider: "p"}, nil
		},
		Invoke: func(context.Context, protocol.InvokeParams) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
		Host: func(_ context.Context, name string, _ json.RawMessage, running bool) (protocol.HostResult, error) {
			return protocol.HostResult{Text: fmt.Sprintf("%s, running %t", name, running)}, nil
		},
	})

	started := make(chan struct{})
	finished := make(chan []protocol.Event, 1)

	go func() {
		var events []protocol.Event

		front.Run("task", func(event protocol.Event) {
			events = append(events, event)

			if event.Text == "started" {
				close(started)
			}
		})

		finished <- events
	}()

	<-started

	// The host answers while the run works, and is told a run is on.
	if answer, err := front.InvokeHost("status", nil); err != nil || answer.Text != "status, running true" {
		t.Fatalf("host function during a run = %+v, err = %v", answer, err)
	}

	// The provider is the run's until it ends.
	if _, err := front.Describe(); err == nil || !strings.Contains(err.Error(), "busy with the run") {
		t.Fatalf("describe during a run: err = %v", err)
	}

	if _, err := front.Invoke("models", nil); err == nil || !strings.Contains(err.Error(), "busy with the run") {
		t.Fatalf("a provider function during a run: err = %v", err)
	}

	// Events carry no request id, so a second run could not be told apart.
	if _, err := front.Run("another", nil); err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("a second run: err = %v", err)
	}

	close(release)

	// The run got all of its events, though other answers came meanwhile.
	if events := <-finished; len(events) != 2 || events[1].Text != "finished" {
		t.Fatalf("run events = %+v", events)
	}

	if answer, err := front.InvokeHost("status", nil); err != nil || answer.Text != "status, running false" {
		t.Fatalf("host function after the run = %+v, err = %v", answer, err)
	}
}

func TestSteerReachesTheRunInProgress(t *testing.T) {
	host, front, _ := connect(t, "")
	finish := make(chan struct{})

	go host.Serve(Handlers{
		Run: func(ctx context.Context, params protocol.RunParams) (string, error) {
			host.Text("started")

			// The loop looks between turns; here it looks until it hears.
			var heard []string

			for len(heard) == 0 {
				host.Take(func(text string) error {
					heard = append(heard, text)
					return nil
				})

				time.Sleep(time.Millisecond)
			}

			host.Text("heard " + strings.Join(heard, ", "))
			<-finish

			return "s1", nil
		},
	})

	if err := front.Steer("too early"); err == nil || !strings.Contains(err.Error(), "no run is in progress") {
		t.Fatalf("a steer without a run: err = %v", err)
	}

	started := make(chan struct{})
	finished := make(chan []protocol.Event, 1)

	go func() {
		var events []protocol.Event

		front.Run("task", func(event protocol.Event) {
			events = append(events, event)

			if event.Text == "started" {
				close(started)
			}
		})

		finished <- events
	}()

	<-started

	// Answered once the run has recorded it.
	if err := front.Steer("use v2"); err != nil {
		t.Fatal(err)
	}

	// What the run never takes fails when the run ends.
	late := make(chan error, 1)

	go func() { late <- front.Steer("too late") }()

	for held := 0; held == 0; time.Sleep(time.Millisecond) {
		host.steering.Lock()
		held = len(host.steers)
		host.steering.Unlock()
	}

	close(finish)

	if err := <-late; err == nil || !strings.Contains(err.Error(), "the run ended before") {
		t.Fatalf("a steer the run never took: err = %v", err)
	}

	if events := <-finished; len(events) != 2 || events[1].Text != "heard use v2" {
		t.Fatalf("run events = %+v", events)
	}

	if err := front.Steer("after"); err == nil || !strings.Contains(err.Error(), "no run is in progress") {
		t.Fatalf("a steer after the run: err = %v", err)
	}
}
