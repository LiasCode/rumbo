// Package tui is rumbo's plugin's terminal front end. It takes the whole
// screen, as rumbo-gui takes a window: the sessions of the directory in a
// sidebar, the conversation of the one shown, and a composer under it with
// the model, how full its context is, and what the session spent; and,
// over them, the command palette, forms, and dialogs built from what the
// host and the provider describe. A rumbo is one session, so it starts one
// for each other session the person opens, and closes those left unused.
//
// It is a Bubble Tea program: what the person does and what the host sends
// become messages, applied one at a time, and the screen is drawn from what
// they left. Nothing waits for the host while a message is applied; what
// asks the host runs as a command, whose answer is one more message, and a
// run's events are sent as messages in the order they came.
package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"

	"github.com/LiasCode/rumbo/protocol"
)

// Run attaches to the host on r and w, takes over the terminal, and returns
// the process exit code.
func Run(r io.Reader, w io.Writer) int {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(os.Stderr, "rumbo: the tui front end needs a terminal; use -f plain")
		return 1
	}

	// The configuration in force is told to the host whether it is right or
	// not, as the protocol asks; what is wrong with it is said here, before
	// the terminal is taken.
	var config Config
	var wrong error
	var inForce json.RawMessage

	conn, info, err := protocol.DialFrontendConfigured(r, w, "tui", func(given json.RawMessage) json.RawMessage {
		config, wrong = configured(given)
		inForce, _ = json.Marshal(config)

		return inForce
	})

	if err != nil {
		fmt.Fprintln(os.Stderr, "rumbo:", err)
		return 1
	}

	colors, err := themeOf(config, os.Getenv("NO_COLOR"))

	if err = errors.Join(wrong, err); err != nil {
		fmt.Fprintf(os.Stderr, "rumbo: the terminal UI's configuration: %v\nrumbo frontend set tui <field>=<value> changes it\n", err)
		return 1
	}

	shown := newApp(conn, info, colors)

	// The terminal UI is served by rumbo itself, which is the program that
	// lists the other sessions and is the host of each.
	if program, err := os.Executable(); err == nil {
		shown.rumbos = rumbos{start: startRumbo(program, inForce), list: listSessions(program), remove: removeSession(program)}
	}

	program := tea.NewProgram(shown)
	shown.send = program.Send

	_, err = program.Run()

	// The rumbos it started end with it, their runs cancelled and recorded.
	shown.shutdown()

	if err != nil && !errors.Is(err, tea.ErrInterrupted) {
		fmt.Fprintln(os.Stderr, "rumbo:", err)
		return 1
	}

	// What stays in the terminal is the command that continues the session.
	if leaving := shown.leaving(); leaving != "" {
		fmt.Println(leaving)
	}

	return 0
}
