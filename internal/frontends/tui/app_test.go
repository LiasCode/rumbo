package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	t    *testing.T
	a    *app
	sent []tea.Msg
}

// sessionFunctions are the host's functions the sidebar calls.
var sessionFunctions = []protocol.Function{
	{Name: "sessions", Group: protocol.GroupSession},
	{Name: "open", Group: protocol.GroupSession, Params: []protocol.Option{{Name: "session", Type: protocol.OptionString}}},
	{Name: "new", Group: protocol.GroupSession},
}

func staged(t *testing.T, handlers frontend.Handlers, info protocol.FrontendInitializeParams) *stage {
	t.Helper()

	shown := &stage{t: t}
	conn := hosted(t, handlers, info)

	shown.a = newApp(conn, info, defaultTheme)
	shown.a.clock = func() time.Time { return time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC) }
	shown.a.send = func(msg tea.Msg) { shown.sent = append(shown.sent, msg) }

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
		msgs := append(s.sent, answered)
		s.sent = nil

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

// A task runs in the session shown: what was asked shows, then what the
// model wrote and the tools it called, how it ended, and the session it
// made, which the sidebar lists and the header names.
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
			return protocol.HostResult{Values: []protocol.Value{{Value: "s1", Label: "list the files"}}}, nil
		},
	}, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any", Functions: sessionFunctions})

	s.typing("list the files")

	if sent.Objective != "list the files" || sent.Session != "" {
		t.Fatalf("sent %+v", sent)
	}

	screen := s.screen()

	if !inOrder(screen, "alpha › list the files", "list the files │", "Looking.", "✓ bash ls", "a.txt", "Done.", "✓ done", "toy · any", "1% of 200k", "1.5k in") {
		t.Fatalf("the screen:\n%s", screen)
	}

	if s.a.session != "s1" || s.a.here() != nil || s.a.conversations["s1"] != s.a.shown {
		t.Fatalf("session %q, run %v", s.a.session, s.a.here())
	}

	if s.a.leaving() == "" || !strings.Contains(s.a.leaving(), "rumbo -s s1") {
		t.Fatalf("leaving %q", s.a.leaving())
	}
}

// Tab goes to the sessions, and Enter shows the one chosen with what it
// holds; the session left keeps what it showed for when it comes back.
func TestASessionChosenInTheSidebarShowsItsHistory(t *testing.T) {
	var mu sync.Mutex

	current := "s1"

	s := staged(t, frontend.Handlers{
		Host: func(_ context.Context, name string, params json.RawMessage) (protocol.HostResult, error) {
			mu.Lock()
			defer mu.Unlock()

			if name == "open" {
				var given map[string]string
				_ = json.Unmarshal(params, &given)
				current = given["session"]
			}

			listed := []protocol.Value{{Value: "s1", Label: "first task"}, {Value: "s2", Label: "second task"}}

			return protocol.HostResult{Values: listed, State: protocol.HostState{Dir: "/home/me/alpha", Session: current, Provider: "toy", Model: "any"}}, nil
		},
		History: func(params protocol.HistoryParams) (protocol.HistoryResult, error) {
			told := map[string][]protocol.Event{
				"s1": {{Type: protocol.EventObjective, Text: "first task"}, {Type: protocol.EventText, Text: "One."}},
				"s2": {{Type: protocol.EventObjective, Text: "second task"}, {Type: protocol.EventText, Text: "Two."}},
			}

			return protocol.HistoryResult{Session: params.Session, Events: told[params.Session]}, nil
		},
	}, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Session: "s1", Provider: "toy", Model: "any", Functions: sessionFunctions})

	s.run(s.a.load(s.a.shown))
	s.run(s.a.list())

	if screen := s.screen(); !inOrder(screen, "alpha › first task", "first task", "One.") {
		t.Fatalf("the screen:\n%s", screen)
	}

	left := s.a.shown

	s.press(tea.KeyTab)
	s.press(tea.KeyDown)

	if s.a.focus != focusSidebar || s.a.sessions.chosen() != "s2" {
		t.Fatalf("focus %d, chosen %q", s.a.focus, s.a.sessions.chosen())
	}

	s.press(tea.KeyEnter)

	if screen := s.screen(); !inOrder(screen, "alpha › second task", "second task", "Two.") || strings.Contains(screen, "One.") {
		t.Fatalf("the screen:\n%s", screen)
	}

	if s.a.focus != focusComposer || s.a.session != "s2" || s.a.conversations["s1"] != left || len(left.entries) != 2 {
		t.Fatalf("focus %d, session %q", s.a.focus, s.a.session)
	}

	// A click on the first session shows it again, as it was.
	s.click(3, bodyTop+sidebarSessionsRow)

	if s.a.session != "s1" || s.a.shown != left {
		t.Fatalf("after the click: session %q", s.a.session)
	}
}

// A command's answer shows under it, labeled with whose it is, and a slash
// opens the menu of the commands.
func TestACommandIsAnsweredWhereItWasTyped(t *testing.T) {
	functions := append([]protocol.Function{{Name: "status", Description: "Show the session", Group: protocol.GroupSession}}, sessionFunctions...)

	s := staged(t, frontend.Handlers{
		Host: func(_ context.Context, name string, _ json.RawMessage) (protocol.HostResult, error) {
			return protocol.HostResult{Text: "provider: toy", State: protocol.HostState{Provider: "toy", Model: "any"}}, nil
		},
		Describe: func(context.Context) (protocol.DescribeResult, error) {
			return protocol.DescribeResult{}, nil
		},
	}, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any", Functions: functions})

	s.apply(tea.KeyPressMsg{Code: '/', Text: "/"})
	s.apply(tea.KeyPressMsg{Code: 's', Text: "s"})
	s.apply(tea.KeyPressMsg{Code: 't', Text: "t"})

	if screen := s.screen(); !inOrder(screen, "/status", "Show the session", "/st") {
		t.Fatalf("the menu:\n%s", screen)
	}

	s.press(tea.KeyEnter)

	if screen := s.screen(); !inOrder(screen, "› /status", "[host]", "provider: toy") || s.a.menu != nil || s.a.asking {
		t.Fatalf("the screen:\n%s", screen)
	}
}

// Every row of the screen is as wide as the screen, and there are as many
// as it is high, with the sidebar or, on a narrow screen, without it.
func TestTheScreenIsFilledExactly(t *testing.T) {
	s := staged(t, frontend.Handlers{}, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Functions: sessionFunctions})

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

		if hasSidebar := strings.Contains(s.screen(), "SESSIONS") || strings.Contains(s.screen(), "no sessions yet"); hasSidebar != (size[0] >= sidebarFrom) {
			t.Fatalf("%dx%d: sidebar %v", size[0], size[1], hasSidebar)
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

// Ctrl-P opens the palette, which narrows as one types; a function chosen
// there that takes parameters asks for them in a form, which is run as the
// command it is, shown in the conversation with its answer.
func TestAFunctionChosenInThePaletteAsksForItsParameters(t *testing.T) {
	called := make(chan json.RawMessage, 1)
	s := staged(t, greeting(called), protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any"})

	s.text("a draft")
	s.apply(ctrl('p'))

	if s.a.palette == nil || !strings.Contains(s.screen(), "greet") {
		t.Fatalf("the palette:\n%s", s.screen())
	}

	s.text("gree")
	s.press(tea.KeyEnter)

	if s.a.palette != nil || s.a.form == nil || len(s.a.form.fields) != 2 {
		t.Fatalf("palette %v, form %+v", s.a.palette, s.a.form)
	}

	// What is needed and missing keeps the form open, saying why.
	s.press(tea.KeyEnter)

	if s.a.form == nil || !strings.Contains(s.screen(), "who is needed") {
		t.Fatalf("the form:\n%s", s.screen())
	}

	s.text("Ana")
	s.press(tea.KeyEnter)

	if params := <-called; string(params) != `{"who":"Ana"}` {
		t.Fatalf("called with %s", params)
	}

	if screen := s.screen(); s.a.form != nil || !inOrder(screen, "› /greet Ana", "[provider toy]", "Hello there", "a draft") {
		t.Fatalf("the screen:\n%s", screen)
	}
}

// A function that cannot be undone is called only once the person agrees,
// in a dialog that starts on the button that leaves things as they are.
func TestAFunctionThatCannotBeUndoneIsConfirmed(t *testing.T) {
	deleted := 0

	functions := []protocol.Function{{
		Name: "delete", Group: protocol.GroupSession, Confirm: "This deletes the session, for good.",
		Params: []protocol.Option{{Name: "session", Type: protocol.OptionString}},
	}}

	s := staged(t, frontend.Handlers{
		Host: func(_ context.Context, name string, _ json.RawMessage) (protocol.HostResult, error) {
			deleted++

			return protocol.HostResult{Text: "deleted session s1", State: protocol.HostState{Dir: "/home/me/alpha", Provider: "toy", Model: "any"}}, nil
		},
	}, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any", Functions: functions})

	s.typing("/delete s1")

	if s.a.confirming == nil || !inOrder(s.screen(), "delete", "This deletes the session, for good.", "Cancel", "Delete") {
		t.Fatalf("the screen:\n%s", s.screen())
	}

	s.press(tea.KeyEnter)

	if deleted != 0 || s.a.confirming != nil || !strings.Contains(s.screen(), "nothing was done") {
		t.Fatalf("deleted %d times:\n%s", deleted, s.screen())
	}

	s.typing("/delete s1")
	s.press(tea.KeyRight)
	s.press(tea.KeyEnter)

	if deleted != 1 || !inOrder(s.screen(), "nothing was done", "› /delete s1", "deleted session s1") {
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

	if screen := s.screen(); !inOrder(screen, "› /use toy toy-2", "› /session-set effort=high", "done: use", "done: session-set", "toy · toy-2") {
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
	s.run(s.a.describe(""))

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

	for _, r := range "my draft" {
		key(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	key(ctrl('p'))

	for _, r := range "login" {
		key(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	key(tea.KeyPressMsg{Code: tea.KeyEnter})
	await(func() bool { return s.a.question != nil })

	for _, r := range "4711" {
		key(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	if screen := s.screen(); !inOrder(screen, "the provider asks", "Paste the code", "› ••••", "my draft") || strings.Contains(screen, "4711") {
		t.Fatalf("the question:\n%s", screen)
	}

	key(tea.KeyPressMsg{Code: tea.KeyEnter})
	await(func() bool { return !s.a.asking })

	if screen := s.screen(); !inOrder(screen, "› /login", "Paste the code", "› (not shown)", "signed in with 4711", "my draft") || s.a.question != nil {
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

	functions := append([]protocol.Function{{Name: "status"}, {Name: "skills"}}, sessionFunctions...)

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

	for _, size := range [][2]int{{160, 40}, {90, 30}} {
		s.apply(tea.WindowSizeMsg{Width: size[0], Height: size[1]})

		rows := strings.Split(s.a.View().Content, "\n")

		for _, row := range rows {
			if visibleWidth(row) != size[0] {
				t.Fatalf("%dx%d: a row of %d columns: %q", size[0], size[1], visibleWidth(row), stripped(row))
			}
		}

		// The rule under the header meets each border under it.
		if joints, want := strings.Count(rows[1], "┬"), 1+map[bool]int{true: 1}[size[0] == 160]; joints != want {
			t.Fatalf("%dx%d: the rule meets %d borders, not %d: %q", size[0], size[1], joints, want, stripped(rows[1]))
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
// and back, and what the person asked can be asked again in a branch that
// starts before it, which runs once the branch is shown.
func TestAMessageIsAskedAgainInABranch(t *testing.T) {
	var mu sync.Mutex

	var branched string

	var ran protocol.RunParams

	functions := append([]protocol.Function{{Name: "branch", Params: []protocol.Option{{Name: "point", Type: protocol.OptionString, Optional: true}}}}, sessionFunctions...)

	s := staged(t, frontend.Handlers{
		Host: func(_ context.Context, name string, params json.RawMessage) (protocol.HostResult, error) {
			mu.Lock()
			defer mu.Unlock()

			session := "s1"

			if name == "branch" {
				branched, session = string(params), "s2"
			}

			if branched != "" {
				session = "s2"
			}

			return protocol.HostResult{Text: name + " done", State: protocol.HostState{Dir: "/home/me/alpha", Session: session, Provider: "toy", Model: "any"}}, nil
		},
		History: func(params protocol.HistoryParams) (protocol.HistoryResult, error) {
			if params.Session != "s1" {
				return protocol.HistoryResult{Session: params.Session}, nil
			}

			return protocol.HistoryResult{Session: "s1", Events: []protocol.Event{
				{Type: protocol.EventObjective, Text: "count the files"},
				{Type: protocol.EventToolStarted, Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)},
				{Type: protocol.EventToolFinished, Name: "bash", Result: "1\n2\n3\n4\n5\n6\n7\n8\n9"},
				{Type: protocol.EventText, Text: "Nine."},
				{Type: protocol.EventEnded, Status: protocol.RunDone},
			}}, nil
		},
		Run: func(params protocol.RunParams, run *frontend.Run) (frontend.Work, error) {
			ran = params

			return func(context.Context) (string, error) {
				run.Attach(params.Session)
				run.Text("Nine again.")

				return params.Session, nil
			}, nil
		},
	}, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Session: "s1", Provider: "toy", Model: "any", Functions: functions})

	s.run(s.a.load(s.a.shown))

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
	s.press(tea.KeyEnter)

	if s.a.palette == nil || !inOrder(s.screen(), "your message", "Change it", "Ask it again", "Copy it") {
		t.Fatalf("what can be done with the message:\n%s", s.screen())
	}

	s.press(tea.KeyDown)
	s.press(tea.KeyEnter)

	if branched != `{"point":"1"}` || ran.Objective != "count the files" || ran.Session != "s2" {
		t.Fatalf("branched with %s, ran %+v", branched, ran)
	}

	if screen := s.screen(); s.a.session != "s2" || !inOrder(screen, "count the files", "Nine again.") || strings.Contains(screen, "Nine.") {
		t.Fatalf("session %q:\n%s", s.a.session, screen)
	}

	if left := s.a.conversations["s1"]; !inOrder(plainRows(left, 80), "› /branch 1", "branch done") {
		t.Fatalf("the session left:\n%s", plainRows(left, 80))
	}
}

// Delete in the sidebar deletes the session chosen there, once the person
// agrees.
func TestASessionIsDeletedFromTheSidebar(t *testing.T) {
	functions := append([]protocol.Function{{Name: "delete", Confirm: "This deletes it.", Params: []protocol.Option{{Name: "session", Type: protocol.OptionString}}}}, sessionFunctions...)

	var deleted string

	s := staged(t, frontend.Handlers{
		Host: func(_ context.Context, name string, params json.RawMessage) (protocol.HostResult, error) {
			if name == "delete" {
				deleted = string(params)
			}

			return protocol.HostResult{Values: []protocol.Value{{Value: "s1", Label: "first"}, {Value: "s2", Label: "second"}}, State: protocol.HostState{Dir: "/home/me/alpha", Provider: "toy", Model: "any"}}, nil
		},
	}, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any", Functions: functions})

	s.run(s.a.list())
	s.press(tea.KeyTab)
	s.press(tea.KeyDown)
	s.press(tea.KeyDown)
	s.press(tea.KeyDelete)

	if s.a.confirming == nil || deleted != "" {
		t.Fatalf("confirming %v, deleted %q", s.a.confirming, deleted)
	}

	s.press(tea.KeyRight)
	s.press(tea.KeyEnter)

	if deleted != `{"session":"s2"}` {
		t.Fatalf("deleted %q", deleted)
	}
}

// What the person asked can be changed in a branch that starts before it:
// the branch is shown, with what was asked in the composer, to change and
// send.
func TestAMessageIsChangedInABranch(t *testing.T) {
	functions := append([]protocol.Function{{Name: "branch", Params: []protocol.Option{{Name: "point", Type: protocol.OptionString, Optional: true}}}}, sessionFunctions...)

	var branched string

	s := staged(t, frontend.Handlers{
		Host: func(_ context.Context, name string, params json.RawMessage) (protocol.HostResult, error) {
			if name == "branch" {
				branched = string(params)
			}

			return protocol.HostResult{State: protocol.HostState{Dir: "/home/me/alpha", Session: "s2", Provider: "toy", Model: "any"}}, nil
		},
		History: func(params protocol.HistoryParams) (protocol.HistoryResult, error) {
			told := map[string][]protocol.Event{"s1": {
				{Type: protocol.EventObjective, Text: "first"},
				{Type: protocol.EventEnded, Status: protocol.RunDone},
				{Type: protocol.EventObjective, Text: "second"},
				{Type: protocol.EventEnded, Status: protocol.RunDone},
			}}

			return protocol.HistoryResult{Session: params.Session, Events: told[params.Session]}, nil
		},
	}, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Session: "s1", Provider: "toy", Model: "any", Functions: functions})

	s.run(s.a.load(s.a.shown))
	s.apply(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	s.press(tea.KeyEnter)
	s.press(tea.KeyEnter)

	if branched != `{"point":"2"}` || s.a.session != "s2" || s.a.input.String() != "second" || s.a.focus != focusComposer {
		t.Fatalf("branched with %s; session %q, composer %q, focus %d", branched, s.a.session, s.a.input.String(), s.a.focus)
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

// A run that ends in a session not shown marks it in the sidebar and says
// so; and while the terminal is not in front, the bell rings and the
// desktop is told, with OSC 9.
func TestARunThatEndsAwayIsSaidAndNotified(t *testing.T) {
	s := staged(t, frontend.Handlers{
		Host: func(context.Context, string, json.RawMessage) (protocol.HostResult, error) {
			return protocol.HostResult{Values: []protocol.Value{{Value: "s1", Label: "here"}, {Value: "s2", Label: "away"}}}, nil
		},
	}, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Session: "s1", Provider: "toy", Model: "any", Functions: sessionFunctions})

	s.run(s.a.load(s.a.shown))
	s.run(s.a.list())

	run := &flight{session: "s2", label: "fix it", conv: s.a.conversationOf("s2")}
	s.a.runs = append(s.a.runs, run)

	if !strings.Contains(s.screen(), "1 working elsewhere") {
		t.Fatalf("the run away is not counted:\n%s", s.screen())
	}

	s.apply(tea.BlurMsg{})

	_, cmd := s.a.Update(endedMsg{run, protocol.RunResult{Status: protocol.RunDone, Session: "s2"}, nil})

	notified := false

	for _, msg := range messagesOf(cmd) {
		notified = notified || strings.Contains(fmt.Sprint(msg), "\a"+escape+"]9;rumbo: done · fix it\a")
	}

	if !notified || !s.a.unseen["s2"] || !strings.Contains(s.screen(), "the run in “away” ended: done") {
		t.Fatalf("notified %v, unseen %v:\n%s", notified, s.a.unseen, s.screen())
	}

	// A run that ends while the terminal is in front rings nothing.
	s.apply(tea.FocusMsg{})

	if s.a.notify("anything") != nil {
		t.Fatal("notified in front")
	}
}
