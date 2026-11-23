// Package plain is the built-in front end for pipes and scripts: the model's
// text goes to stdout and progress goes to stderr, with no terminal control.
package plain

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strings"

	"github.com/LiasCode/rumbo/internal/frontends/render"
	"github.com/LiasCode/rumbo/protocol"
)

// Run attaches to the host on r and w and returns the process exit code.
func Run(r io.Reader, w io.Writer) int {
	var given json.RawMessage

	conn, info, err := protocol.DialFrontendConfigured(r, w, "plain", func(chosen json.RawMessage) json.RawMessage {
		given = chosen

		return nil
	})

	if err == nil {
		err = refused(given)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "rumbo:", err)
		return 1
	}

	front := &frontend{conn: conn, out: os.Stdout, status: os.Stderr}

	if info.Resume {
		return front.run(front.conn.Resume)
	}

	if info.Objective != "" {
		return front.objective(info.Objective)
	}

	if !isTerminal(os.Stdin) {
		piped, err := io.ReadAll(os.Stdin)
		objective := strings.TrimSpace(string(piped))

		if err != nil || objective == "" {
			fmt.Fprintln(os.Stderr, "rumbo: no objective given")
			return 1
		}

		return front.objective(objective)
	}

	return front.prompt()
}

// refused says why plain cannot start with what was chosen for it: it has
// no configuration, so any field is one it does not have. The host keeps
// what it is given unchecked, since a front end checks it when it starts.
func refused(given json.RawMessage) error {
	var fields map[string]json.RawMessage

	if len(given) == 0 {
		return nil
	}

	if err := json.Unmarshal(given, &fields); err != nil {
		return fmt.Errorf("the configuration of plain: %w", err)
	}

	if len(fields) == 0 {
		return nil
	}

	names := slices.Sorted(maps.Keys(fields))

	return fmt.Errorf(`plain has no configuration, and was given %s; "rumbo frontend set plain %s=" takes it out`, strings.Join(names, ", "), names[0])
}

type frontend struct {
	conn   *protocol.FrontendConn
	out    io.Writer
	status io.Writer
	// midLine is true while the last text written did not end its line.
	midLine bool
	// announced is true once the session id has been shown.
	announced bool
}

// objective works on one objective.
func (f *frontend) objective(text string) int {
	return f.run(func(onEvent func(protocol.Event)) (protocol.RunResult, error) {
		return f.conn.Run(text, onEvent)
	})
}

// run follows one run, started by start, to its end. Ctrl-C cancels it.
func (f *frontend) run(start func(onEvent func(protocol.Event)) (protocol.RunResult, error)) int {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	defer signal.Stop(interrupt)

	done := make(chan struct{})
	defer close(done)

	go func() {
		select {
		case <-interrupt:
			f.conn.Cancel()
		case <-done:
		}
	}()

	var usage render.Usage

	result, err := start(func(event protocol.Event) {
		usage.Turn(event)
		f.show(event)
	})

	if err != nil {
		f.line("rumbo: %s", err)
		return 1
	}

	if !f.announced && result.Session != "" {
		f.line("session: %s", result.Session)
		f.announced = true
	}

	f.line("tokens: %s", &usage)

	if context := usage.Context(); context != "" {
		f.line("%s", context)
	}

	switch result.Status {
	case protocol.RunDone:
		return 0
	case protocol.RunCancelled:
		f.line("cancelled")
		return 130
	}

	f.line("rumbo: %s", result.Error)

	return 1
}

// prompt reads objectives line by line until end of input. A failed run does
// not end the prompt.
func (f *frontend) prompt() int {
	lines := bufio.NewScanner(os.Stdin)
	lines.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for {
		fmt.Fprint(f.status, "\n> ")

		if !lines.Scan() {
			fmt.Fprintln(f.status)
			return 0
		}

		if objective := strings.TrimSpace(lines.Text()); objective != "" {
			f.objective(objective)
		}
	}
}

func (f *frontend) show(event protocol.Event) {
	switch event.Type {
	case protocol.EventText:
		fmt.Fprint(f.out, event.Text)
		f.midLine = !strings.HasSuffix(event.Text, "\n")

	case protocol.EventContext:
		f.line("context: %s", strings.Join(event.Sections, ", "))

	case protocol.EventToolStarted:
		f.line("> %s", render.Tool(event.Name, event.Args))

	case protocol.EventToolFinished:
		if event.Failed {
			f.line("  failed: %s", render.FirstLine(event.Result))
		}

	case protocol.EventLog:
		f.line("%s", event.Text)
	}
}

func (f *frontend) line(format string, args ...any) {
	if f.midLine {
		fmt.Fprintln(f.out)
		f.midLine = false
	}

	fmt.Fprintf(f.status, format+"\n", args...)
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()

	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
