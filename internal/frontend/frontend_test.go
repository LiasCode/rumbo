package frontend

import (
	"context"
	"errors"
	"io"
	"testing"

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
		served <- host.Serve(func(ctx context.Context, params protocol.RunParams) (string, error) {
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
		})
	}()

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
