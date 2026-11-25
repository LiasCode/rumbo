package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/LiasCode/rumbo/internal/frontend"
	"github.com/LiasCode/rumbo/protocol"
)

// stage is the terminal UI attached to a host that handlers answer, on a
// screen 120 columns by 40 rows. What it asks the host is done at once and
// in order, as the program would, and what a request sends while it is
// open comes before its answer.
type stage struct {
	t *testing.T
	a *app
	// sending guards sent, which a rumbo that ends adds to from a
	// goroutine of its own.
	sending sync.Mutex
	sent    []tea.Msg
	// desktop is what was put in the desktop's clipboard, and its primary
	// selection, which a test never reaches.
	desktop []string
}

func staged(t *testing.T, handlers frontend.Handlers, info protocol.FrontendInitializeParams) *stage {
	t.Helper()

	shown := &stage{t: t}
	conn := hosted(t, handlers, info)

	shown.a = newApp(conn, info, defaultTheme)
	shown.a.copier = func(_ context.Context, text string, primary bool) (string, error) {
		shown.desktop = append(shown.desktop, map[bool]string{true: "primary: ", false: ""}[primary]+text)

		return "the clipboard", nil
	}
	shown.a.clock = func() time.Time { return time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC) }
	shown.a.send = func(msg tea.Msg) {
		shown.sending.Lock()
		defer shown.sending.Unlock()

		shown.sent = append(shown.sent, msg)
	}

	// The spinners stay still.
	shown.a.ticking = true

	shown.apply(tea.WindowSizeMsg{Width: 120, Height: 40})

	return shown
}

// apply applies msg, and runs what it asks to its end.
func (s *stage) apply(msg tea.Msg) {
	_, cmd := s.a.Update(msg)
	s.run(cmd)
}

// run runs cmd, and what the messages it leads to ask, in order.
func (s *stage) run(cmd tea.Cmd) {
	queue := []tea.Cmd{cmd}

	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]

		if next == nil {
			continue
		}

		answered := next()

		s.sending.Lock()
		msgs := append(s.sent, answered)
		s.sent = nil
		s.sending.Unlock()

		for _, msg := range msgs {
			switch msg := msg.(type) {
			case nil:
			case tea.BatchMsg:
				queue = append(queue, msg...)
			default:
				_, more := s.a.Update(msg)
				queue = append(queue, more)
			}
		}
	}
}

// typing types text and presses Enter.
func (s *stage) typing(text string) {
	for _, r := range text {
		s.apply(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	s.press(tea.KeyEnter)
}

func (s *stage) press(code rune) {
	s.apply(tea.KeyPressMsg{Code: code})
}

// click presses the pointer's left button on a cell and lets it go there.
func (s *stage) click(x, y int) {
	s.apply(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	s.apply(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
}

// screen is what the terminal UI shows, without its styles.
func (s *stage) screen() string {
	rows := strings.Split(s.a.View().Content, "\n")

	for index, row := range rows {
		rows[index] = strings.TrimRight(stripped(row), " ")
	}

	return strings.Join(rows, "\n")
}

// A task runs in the host's session: what was asked shows, then what the
// model wrote and the tools it called, how it ended, and the session it
// made, which is called after the task until the host says what it is
// called.
func TestAFullRunIsShownInItsSession(t *testing.T) {
	var sent protocol.RunParams

	s := staged(t, frontend.Handlers{
		Run: func(params protocol.RunParams, run *frontend.Run) (frontend.Work, error) {
			sent = params

			return func(context.Context) (string, error) {
				run.Attach("s1")
				run.Text("Looking.\n")
				run.ToolStarted("bash", json.RawMessage(`{"command":"ls"}`))
				run.ToolFinished("bash", "a.txt", false)
				run.TurnFinished(protocol.Usage{Input: 1500, Output: 20}, 3000, 200000)
				run.Text("**Done.**")

				return "s1", nil
			}, nil
		},
		Host: func(_ context.Context, name string, _ json.RawMessage) (protocol.HostResult, error) {
			return protocol.HostResult{Text: name + " done", State: protocol.HostState{Dir: "/home/me/alpha", Session: "s1", Title: "Listing", Provider: "toy", Model: "any"}}, nil
		},
	}, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any", Functions: []protocol.Function{{Name: "rename", Group: protocol.GroupSession}}})

	if screen := s.screen(); !strings.Contains(screen, "Give rumbo a task") || titleOf(s.a.host) != "new session" {
		t.Fatalf("the screen of a new session:\n%s", screen)
	}

	s.typing("list the files")

	if sent.Objective != "list the files" {
		t.Fatalf("sent %+v", sent)
	}

	screen := s.screen()

	if !inOrder(screen, "list the files", "Looking.", "✓ bash ls", "a.txt", "Done.", "✓ done", "toy · any", "1% of 200k", "1.5k in") || titleOf(s.a.host) != "list the files" {
		t.Fatalf("the screen:\n%s", screen)
	}

	if s.a.session != "s1" || s.a.here() != nil || s.a.shown.session != "s1" {
		t.Fatalf("session %q, run %v", s.a.session, s.a.here())
	}

	if s.a.leaving() == "" || !strings.Contains(s.a.leaving(), "rumbo -s s1") {
		t.Fatalf("leaving %q", s.a.leaving())
	}

	// What the host says the session is called is what it is called.
	s.typing("/rename Listing")

	if screen := s.screen(); !inOrder(screen, "Looking.", "› /rename Listing", "rename done") || titleOf(s.a.host) != "Listing" {
		t.Fatalf("the screen once the host named the session:\n%s", screen)
	}
}

// A session that is continued shows what it holds, under what it is called.
func TestAContinuedSessionShowsItsHistory(t *testing.T) {
	s := staged(t, frontend.Handlers{
		History: func() (protocol.HistoryResult, error) {
			return protocol.HistoryResult{Session: "s1", Events: []protocol.Event{{Type: protocol.EventObjective, Text: "first task"}, {Type: protocol.EventText, Text: "One."}}}, nil
		},
	}, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Session: "s1", Title: "First", Provider: "toy", Model: "any"})

	if screen := s.screen(); !strings.Contains(screen, "reading the session") {
		t.Fatalf("the screen before the history comes:\n%s", screen)
	}

	s.run(s.a.load(s.a.host))

	if screen := s.screen(); !inOrder(screen, "first task", "One.") {
		t.Fatalf("the screen:\n%s", screen)
	}
}

// A command's answer shows under it, labeled with whose it is, and a slash
// opens the menu of the commands. A function of the host no command wraps,
// as a newer host has, is a command under its name.
func TestACommandIsAnsweredWhereItWasTyped(t *testing.T) {
	functions := []protocol.Function{{Name: "doctor", Description: "Check what is set up", Group: protocol.GroupSystem}}

	s := staged(t, frontend.Handlers{
		Host: func(_ context.Context, name string, _ json.RawMessage) (protocol.HostResult, error) {
			return protocol.HostResult{Text: "provider: toy", State: protocol.HostState{Provider: "toy", Model: "any"}}, nil
		},
		Describe: func(context.Context) (protocol.DescribeResult, error) {
			return protocol.DescribeResult{}, nil
		},
	}, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any", Functions: functions})

	s.text("/doc")

	if screen := s.screen(); !inOrder(screen, "/doctor", "Check what is set up", "/doc") {
		t.Fatalf("the menu:\n%s", screen)
	}

	s.press(tea.KeyEnter)

	if screen := s.screen(); !inOrder(screen, "› /doctor", "[host]", "provider: toy") || s.a.menu != nil || s.a.asking {
		t.Fatalf("the screen:\n%s", screen)
	}
}

// Every row of the screen is as wide as the screen, and there are as many
// as it is high.
func TestTheScreenIsFilledExactly(t *testing.T) {
	s := staged(t, frontend.Handlers{}, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy"})

	s.a.shown.ask("a task that is long enough to wrap in a narrow column of the conversation", nil, false, s.a.now)
	s.a.shown.apply(protocol.Event{Type: protocol.EventText, Text: "| a | b |\n|---|---|\n| 1 | 2 |\n"}, false, s.a.now)

	for _, size := range [][2]int{{120, 40}, {60, 20}, {200, 50}, {30, 10}} {
		s.apply(tea.WindowSizeMsg{Width: size[0], Height: size[1]})

		rows := strings.Split(s.a.View().Content, "\n")

		if len(rows) != size[1] {
			t.Fatalf("%dx%d: %d rows", size[0], size[1], len(rows))
		}

		for _, row := range rows {
			if visibleWidth(row) != size[0] {
				t.Fatalf("%dx%d: a row of %d columns: %q", size[0], size[1], visibleWidth(row), stripped(row))
			}
		}
	}
}

// ctrl is a key pressed with Ctrl.
func ctrl(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl}
}

// text types text, without pressing Enter.
func (s *stage) text(text string) {
	for _, r := range text {
		s.apply(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// greeting is a provider that offers a function with a parameter and one
// that may be left out, and answers it in words.
func greeting(called chan<- json.RawMessage) frontend.Handlers {
	return frontend.Handlers{
		Describe: func(context.Context) (protocol.DescribeResult, error) {
			return protocol.DescribeResult{Provider: "toy", Offers: protocol.Description{Functions: []protocol.Function{
				{Name: "greet", Description: "Say hello", Params: []protocol.Option{
					{Name: "who", Type: protocol.OptionString},
					{Name: "times", Type: protocol.OptionNumber, Optional: true},
				}},
			}}}, nil
		},
		Invoke: func(_ context.Context, params protocol.InvokeParams, _ protocol.Dialog) (json.RawMessage, error) {
			called <- params.Params

			return json.RawMessage(`"Hello there"`), nil
		},
	}
}

// Ctrl-P starts a command on an empty line, as a slash does: the menu lists
// the terminal UI's commands, which typing narrows. A function of the
// provider is no command of its own, but is called after the provider's
// name, offered there, and runs as the command it is, shown in the
// conversation with its answer.
func TestCommandsAreTypedInTheComposer(t *testing.T) {
	called := make(chan json.RawMessage, 1)
	s := staged(t, greeting(called), protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any"})

	s.text("a draft")
	s.apply(ctrl('p'))

	if s.a.input.String() != "a draft" || !strings.Contains(s.a.notice, "start with a slash") {
		t.Fatalf("Ctrl-P over a draft: typed %q, notice %q", s.a.input.String(), s.a.notice)
	}

	s.a.input.clear()
	s.run(s.a.describe(s.a.host, ""))
	s.apply(ctrl('p'))

	if s.a.input.String() != "/" || s.a.menu == nil || !inOrder(s.screen(), "/new", "/sessions", "/resume") {
		t.Fatalf("the menu, typed %q:\n%s", s.a.input.String(), s.screen())
	}

	s.text("prov")

	if shown := s.a.menu.shown; len(shown) != 1 || shown[0].value != "/providers" {
		t.Fatalf("the menu narrowed: %+v", shown)
	}

	s.a.input.clear()
	s.a.menu = nil
	s.text("/providers toy ")

	if !slices.ContainsFunc(s.a.menu.shown, func(each choice) bool { return each.value == "greet" && each.description == "Say hello" }) {
		t.Fatalf("what the provider offers: %+v", s.a.menu.shown)
	}

	s.a.input.clear()
	s.a.menu = nil
	s.typing("/greet Ana")

	if len(called) != 0 || !strings.Contains(s.screen(), "there is no command /greet") {
		t.Fatalf("/greet is a command:\n%s", s.screen())
	}

	// What the provider offers is asked first when it is not known.
	s.a.described = nil
	s.typing("/providers toy greet Ana")

	if params := <-called; string(params) != `{"who":"Ana"}` {
		t.Fatalf("called with %s", params)
	}

	if screen := s.screen(); !inOrder(screen, "› /providers toy greet Ana", "[provider toy]", "Hello there") {
		t.Fatalf("the screen:\n%s", screen)
	}
}

// A function that cannot be undone is called only once the person agrees,
// in a dialog that starts on the button that leaves things as they are.
func TestAFunctionThatCannotBeUndoneIsConfirmed(t *testing.T) {
	deleted := 0

	functions := []protocol.Function{{
		Name: "forget", Group: protocol.GroupSession, Confirm: "This forgets the session, for good.",
		Params: []protocol.Option{{Name: "session", Type: protocol.OptionString}},
	}}

	s := staged(t, frontend.Handlers{
		Host: func(_ context.Context, name string, _ json.RawMessage) (protocol.HostResult, error) {
			deleted++

			return protocol.HostResult{Text: "deleted session s1", State: protocol.HostState{Dir: "/home/me/alpha", Provider: "toy", Model: "any"}}, nil
		},
	}, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any", Functions: functions})

	s.typing("/forget s1")

	if s.a.confirming == nil || !inOrder(s.screen(), "forget", "This forgets the session, for good.", "Cancel", "Forget") {
		t.Fatalf("the screen:\n%s", s.screen())
	}

	s.press(tea.KeyEnter)

	if deleted != 0 || s.a.confirming != nil || !strings.Contains(s.screen(), "nothing was done") {
		t.Fatalf("deleted %d times:\n%s", deleted, s.screen())
	}

	s.typing("/forget s1")
	s.press(tea.KeyRight)
	s.press(tea.KeyEnter)

	if deleted != 1 || !inOrder(s.screen(), "nothing was done", "› /forget s1", "deleted session s1") {
		t.Fatalf("deleted %d times:\n%s", deleted, s.screen())
	}
}

// The picker opens with the model in use and the options in force, and
// what was changed in it is asked of the host one change at a time, each
// shown as the command it is.
func TestThePickerChangesTheModelAndAnOption(t *testing.T) {
	var asked []string

	functions := []protocol.Function{
		{Name: "use", Group: protocol.GroupSession, Params: []protocol.Option{
			{Name: "provider", Type: protocol.OptionString},
			{Name: "model", Type: protocol.OptionString},
		}},
		{Name: "session-set", Group: protocol.GroupSession, Params: []protocol.Option{{Name: "change", Type: protocol.OptionString}}},
	}

	s := staged(t, frontend.Handlers{
		Describe: func(context.Context) (protocol.DescribeResult, error) {
			return protocol.DescribeResult{Provider: "toy", Model: "toy-1", Offers: protocol.Description{Options: []protocol.Option{
				{Name: "effort", Type: protocol.OptionString, Enum: []string{"low", "high"}, Default: json.RawMessage(`"low"`)},
			}}}, nil
		},
		Host: func(_ context.Context, name string, params json.RawMessage) (protocol.HostResult, error) {
			asked = append(asked, name+" "+string(params))

			return protocol.HostResult{Text: "done: " + name, State: protocol.HostState{Dir: "/home/me/alpha", Provider: "toy", Model: "toy-2"}}, nil
		},
	}, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "toy-1", Functions: functions})

	// A click on the model under the composer opens it.
	shown := s.a.layout()
	s.click(shown.columnX+3, shown.composerTop+len(shown.composer)-2)

	if s.a.form == nil || strings.Join(s.a.form.values(), ",") != "toy,toy-1,low" {
		t.Fatalf("the picker:\n%s", s.screen())
	}

	s.press(tea.KeyTab)
	s.apply(ctrl('u'))
	s.text("toy-2")
	s.press(tea.KeyTab)
	s.press(tea.KeyDown)
	s.press(tea.KeyEnter)

	if strings.Join(asked, "; ") != `use {"model":"toy-2","provider":"toy"}; session-set {"change":"effort=high"}` {
		t.Fatalf("asked %q", asked)
	}

	if screen := s.screen(); !inOrder(screen, "› /model toy/toy-2", "› /model effort=high", "done: use", "done: session-set", "toy · toy-2") {
		t.Fatalf("the screen:\n%s", screen)
	}
}

// A question of a provider's function opens over everything, with a line
// of its own for the answer, so what was being typed in the composer
// stays; a secret answer shows as dots, and is neither shown nor kept.
func TestTheProviderAsksInADialog(t *testing.T) {
	handlers := frontend.Handlers{
		Describe: func(context.Context) (protocol.DescribeResult, error) {
			return protocol.DescribeResult{Provider: "toy", Offers: protocol.Description{Functions: []protocol.Function{{Name: "login"}}}}, nil
		},
		Invoke: func(ctx context.Context, _ protocol.InvokeParams, dialog protocol.Dialog) (json.RawMessage, error) {
			code, err := dialog.Ask(ctx, "Paste the code", true)

			if err != nil {
				return nil, err
			}

			return json.Marshal("signed in with " + code)
		},
	}

	s := staged(t, handlers, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any"})
	s.run(s.a.describe(s.a.host, ""))

	// The function waits for the answer, so what it asks runs in the
	// background, as the program runs it.
	inbox := make(chan tea.Msg, 64)
	s.a.send = func(msg tea.Msg) { inbox <- msg }

	start := func(cmd tea.Cmd) {
		if cmd != nil {
			go func() { inbox <- cmd() }()
		}
	}

	key := func(msg tea.KeyPressMsg) {
		_, cmd := s.a.Update(msg)
		start(cmd)
	}

	await := func(done func() bool) {
		deadline := time.After(5 * time.Second)

		for !done() {
			select {
			case msg := <-inbox:
				if batch, ok := msg.(tea.BatchMsg); ok {
					for _, each := range batch {
						start(each)
					}

					continue
				}

				_, cmd := s.a.Update(msg)
				start(cmd)
			case <-deadline:
				t.Fatalf("waited too long:\n%s", s.screen())
			}
		}
	}

	for _, r := range "/login" {
		key(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	// Enter takes /login from the menu, and sends it.
	key(tea.KeyPressMsg{Code: tea.KeyEnter})
	key(tea.KeyPressMsg{Code: tea.KeyEnter})
	await(func() bool { return s.a.question != nil })

	for _, r := range "4711" {
		key(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	if screen := s.screen(); !inOrder(screen, "the provider asks", "Paste the code", "› ••••") || strings.Contains(screen, "4711") {
		t.Fatalf("the question:\n%s", screen)
	}

	key(tea.KeyPressMsg{Code: tea.KeyEnter})
	await(func() bool { return !s.a.asking })

	if screen := s.screen(); !inOrder(screen, "› /login", "Paste the code", "› (not shown)", "signed in with 4711") || s.a.question != nil {
		t.Fatalf("the screen:\n%s", screen)
	}

	for _, entry := range s.a.history.entries {
		if strings.Contains(entry, "4711") {
			t.Fatalf("the answer is kept: %q", s.a.history.entries)
		}
	}
}

// Ctrl-G shows what changed in the project's folder beside the
// conversation, and Ctrl-Alt-B the session's details, with what the host
// says of it laid out by its shape; a screen too narrow for both gives the
// panel the conversation's place, and the keys.
func TestThePanelsShowBesideTheConversation(t *testing.T) {
	dir := scratchRepository(t)

	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("one\n2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	functions := []protocol.Function{{Name: "status"}, {Name: "skills"}}

	s := staged(t, frontend.Handlers{
		Host: func(_ context.Context, name string, _ json.RawMessage) (protocol.HostResult, error) {
			said := map[string]string{
				"status": "provider: toy\ncontext: 1200 tokens",
				"skills": "the model is told of these skills:\n  review: Read a change as a reviewer would",
			}

			return protocol.HostResult{Text: said[name]}, nil
		},
	}, protocol.FrontendInitializeParams{Dir: dir, Provider: "toy", Model: "any", Functions: functions})

	s.apply(ctrl('g'))

	if screen := s.screen(); !inOrder(screen, "Changes", "on main · 1 file", "notes.txt", "-two", "+2") {
		t.Fatalf("the changes:\n%s", screen)
	}

	s.apply(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl | tea.ModAlt})

	if screen := s.screen(); !inOrder(screen, "Session", "SESSION", "New; its first task starts it.", "MODEL", "toy · any", "SKILLS", "• review  Read a change", "EVERYTHING RUMBO REPORTS", "provider", "toy") {
		t.Fatalf("the details:\n%s", screen)
	}

	for _, size := range [][2]int{{160, 40}, {70, 30}} {
		s.apply(tea.WindowSizeMsg{Width: size[0], Height: size[1]})

		rows := strings.Split(s.a.View().Content, "\n")

		for _, row := range rows {
			if visibleWidth(row) != size[0] {
				t.Fatalf("%dx%d: a row of %d columns: %q", size[0], size[1], visibleWidth(row), stripped(row))
			}
		}
	}

	if !s.a.layout().covered || strings.Contains(s.screen(), "Give rumbo a task") {
		t.Fatalf("on a narrow screen, the panel is beside the conversation:\n%s", s.screen())
	}

	s.apply(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl | tea.ModAlt})

	if s.a.panels.shown != panelNone || !strings.Contains(s.screen(), "Give rumbo a task") {
		t.Fatalf("the panel stays:\n%s", s.screen())
	}
}

// Shift-Tab goes up into the conversation, where Enter shows all of a call
// and back, and copies anything else, what the person asked too.
func TestACallIsShownWholeFromTheConversation(t *testing.T) {
	s := staged(t, frontend.Handlers{
		History: func() (protocol.HistoryResult, error) {
			return protocol.HistoryResult{Session: "s1", Events: []protocol.Event{
				{Type: protocol.EventObjective, Text: "count the files"},
				{Type: protocol.EventToolStarted, Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)},
				{Type: protocol.EventToolFinished, Name: "bash", Result: "1\n2\n3\n4\n5\n6\n7\n8\n9"},
				{Type: protocol.EventText, Text: "Nine."},
				{Type: protocol.EventEnded, Status: protocol.RunDone},
			}}, nil
		},
	}, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Session: "s1", Provider: "toy", Model: "any"})

	s.run(s.a.load(s.a.host))

	if screen := s.screen(); !strings.Contains(screen, "… 3 more lines") {
		t.Fatalf("the call shows all of itself before it is asked to:\n%s", screen)
	}

	s.apply(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	s.press(tea.KeyUp)

	if s.a.focus != focusConversation || s.a.shown.entries[s.a.shown.selected].kind != entryTool {
		t.Fatalf("focus %d, chosen %d", s.a.focus, s.a.shown.selected)
	}

	s.press(tea.KeyEnter)

	if screen := s.screen(); !inOrder(screen, "command: ls", "── what it returned", "7", "8", "9", "Nine.") {
		t.Fatalf("the whole call:\n%s", screen)
	}

	s.press(tea.KeyHome)

	if !strings.Contains(s.screen(), "Enter copies it") {
		t.Fatalf("what Enter does on the message:\n%s", s.screen())
	}

	s.press(tea.KeyEnter)

	if s.a.actions != nil || !strings.Contains(s.a.notice, "copied 1 line(s)") {
		t.Fatalf("Enter on the message: actions %v, notice %q", s.a.actions, s.a.notice)
	}
}

// messagesOf runs cmd, and the commands of a batch, and returns what they
// answer.
func messagesOf(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}

	answered := cmd()

	if batch, ok := answered.(tea.BatchMsg); ok {
		var all []tea.Msg

		for _, each := range batch {
			all = append(all, messagesOf(each)...)
		}

		return all
	}

	return []tea.Msg{answered}
}

// When a run ends while the terminal is not in front, the bell rings and
// the desktop is told, with OSC 9.
func TestARunThatEndsAwayIsNotified(t *testing.T) {
	s := staged(t, frontend.Handlers{}, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any"})

	run := &flight{host: s.a.host, label: "fix it", conv: s.a.shown}
	s.a.runs = append(s.a.runs, run)

	s.apply(tea.BlurMsg{})

	_, cmd := s.a.Update(endedMsg{run, protocol.RunResult{Status: protocol.RunDone, Session: "s1"}, nil})

	notified := false

	for _, msg := range messagesOf(cmd) {
		notified = notified || strings.Contains(fmt.Sprint(msg), "\a"+escape+"]9;rumbo: done · fix it\a")
	}

	if !notified || s.a.session != "s1" || s.a.here() != nil {
		t.Fatalf("notified %v, session %q, run %v", notified, s.a.session, s.a.here())
	}

	// A run that ends while the terminal is in front rings nothing.
	s.apply(tea.FocusMsg{})

	if s.a.notify("anything") != nil {
		t.Fatal("notified in front")
	}
}

// An extension's functions are called after its name, said by the host
// with what the provider offers; one is called at the extension, and what
// it answers is marked as the extension's.
func TestAnExtensionsFunctionsAreCalledAfterIt(t *testing.T) {
	handlers := frontend.Handlers{
		Describe: func(context.Context) (protocol.DescribeResult, error) {
			return protocol.DescribeResult{Provider: "toy", Extensions: []protocol.ExtensionOffer{{Name: "notes", Functions: []protocol.Function{
				{Name: "forget", Description: "Forget every note"},
				{Name: "tags", Lists: true},
			}}}}, nil
		},
		Invoke: func(_ context.Context, params protocol.InvokeParams, _ protocol.Dialog) (json.RawMessage, error) {
			return json.Marshal(params.Target + " " + params.Extension + " was asked to " + params.Name)
		},
		Host: func(_ context.Context, name string, _ json.RawMessage) (protocol.HostResult, error) {
			return protocol.HostResult{Text: "notes: forget and remember"}, nil
		},
	}

	functions := []protocol.Function{{Name: "extension-values", Lists: true, Params: []protocol.Option{{Name: "extension", Type: protocol.OptionString}}}}
	s := staged(t, handlers, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any", Functions: functions})
	s.run(s.a.describe(s.a.host, ""))

	// An extension's functions are no commands of their own; one there to
	// list values is not offered at all.
	s.text("/extensions notes ")

	if !slices.ContainsFunc(s.a.menu.shown, func(each choice) bool { return each.value == "forget" }) || slices.ContainsFunc(s.a.menu.shown, func(each choice) bool { return each.value == "tags" }) {
		t.Fatalf("the choices: %+v", s.a.menu.shown)
	}

	s.a.input.clear()
	s.a.menu = nil
	s.typing("/extensions notes forget")

	if screen := s.screen(); !inOrder(screen, "› /extensions notes forget", "[extension notes]", "extension notes was asked to forget") {
		t.Fatalf("the screen:\n%s", screen)
	}

	// What the function did may have changed what its extension offers, so
	// that is asked again before it is next needed.
	if s.a.described != nil {
		t.Fatal("what the extensions offer is still taken as it was before the function")
	}
}

// A tool of a run tells what it is doing on its line, and what it asks
// opens over everything, naming the tool; Escape declines, which leaves
// the tool to decide and the run going. What an extension prints is marked
// as an extension's.
func TestAToolOfARunAsksInADialog(t *testing.T) {
	handlers := frontend.Handlers{
		Run: func(_ protocol.RunParams, run *frontend.Run) (frontend.Work, error) {
			return func(ctx context.Context) (string, error) {
				run.Attach("s1")
				run.ToolStarted("bash", json.RawMessage(`{"command":"rm -rf build"}`))
				run.Progress("bash", "checking the command")

				answer, err := run.Ask(ctx, "bash", "Run rm -rf build?", false)

				if err != nil {
					answer = "nothing, since " + err.Error()
				}

				run.ToolFinished("bash", "the answer was "+answer, err != nil)

				return "s1", nil
			}, nil
		},
	}

	s := staged(t, handlers, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any"})

	inbox := make(chan tea.Msg, 64)
	s.a.send = func(msg tea.Msg) { inbox <- msg }

	start := func(cmd tea.Cmd) {
		if cmd != nil {
			go func() { inbox <- cmd() }()
		}
	}

	key := func(msg tea.KeyPressMsg) {
		_, cmd := s.a.Update(msg)
		start(cmd)
	}

	await := func(done func() bool) {
		deadline := time.After(5 * time.Second)

		for !done() {
			select {
			case msg := <-inbox:
				if batch, ok := msg.(tea.BatchMsg); ok {
					for _, each := range batch {
						start(each)
					}

					continue
				}

				_, cmd := s.a.Update(msg)
				start(cmd)
			case <-deadline:
				t.Fatalf("waited too long:\n%s", s.screen())
			}
		}
	}

	ask := func() {
		for _, r := range "clean" {
			key(tea.KeyPressMsg{Code: r, Text: string(r)})
		}

		// The events and the question come their own ways, in either order.
		key(tea.KeyPressMsg{Code: tea.KeyEnter})
		await(func() bool {
			index := s.a.shown.workingOn("bash")

			return s.a.question != nil && index >= 0 && s.a.shown.entries[index].call.doing != ""
		})
	}

	ask()

	if screen := s.screen(); !inOrder(screen, "the tool bash asks", "[tool bash] Run rm -rf build?", "Esc declines") || !strings.Contains(screen, "bash rm -rf build  0.0s  checking the command") {
		t.Fatalf("the question:\n%s", screen)
	}

	key(tea.KeyPressMsg{Code: tea.KeyEscape})
	await(func() bool { return s.a.here() == nil })

	// The question and the call come their own ways, so either shows first.
	if screen := s.screen(); !inOrder(screen, "✗ bash rm -rf build", "the answer was nothing, since the person declined to answer", "✓ done") || !inOrder(screen, "Run rm -rf build?", "› (declined)", "✓ done") || s.a.question != nil {
		t.Fatalf("declined:\n%s", screen)
	}

	ask()

	for _, r := range "yes" {
		key(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	key(tea.KeyPressMsg{Code: tea.KeyEnter})
	await(func() bool { return s.a.here() == nil })

	if screen := s.screen(); !inOrder(screen, "✓ bash rm -rf build", "the answer was yes") || !inOrder(screen, "› (declined)", "Run rm -rf build?", "› yes") {
		t.Fatalf("answered:\n%s", screen)
	}

	s.a.shown.apply(protocol.Event{Type: protocol.EventLog, Source: protocol.TargetExtension, Text: "notes: kept one"}, false, s.a.now)

	if screen := s.screen(); !strings.Contains(screen, "[extension] notes: kept one") {
		t.Fatalf("an extension's line:\n%s", screen)
	}
}
