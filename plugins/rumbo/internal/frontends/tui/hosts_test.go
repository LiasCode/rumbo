package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/LiasCode/rumbo/internal/frontend"
	"github.com/LiasCode/rumbo/protocol"
)

// directory is the sessions of a folder, each a rumbo the terminal UI
// starts when it is shown: what was started, stopped, and deleted, in
// order, and what each one's host was asked by name.
type directory struct {
	mu       sync.Mutex
	started  []string
	stopped  []string
	removed  []string
	asked    map[string][]string
	sessions []protocol.Value
	// branch is the session a host's branch function answers with, and
	// points what each call to it was given.
	branch string
	points []string
}

// handlers are what the host of a session answers: its history, one task
// called after it, what its provider offers, and its own functions by name.
func (d *directory) handlers(session string) frontend.Handlers {
	state := protocol.HostState{Dir: "/home/me/alpha", Session: session, Title: d.title(session), Provider: "toy", Model: "any"}

	return frontend.Handlers{
		History: func() (protocol.HistoryResult, error) {
			return protocol.HistoryResult{Session: session, Events: []protocol.Event{{Type: protocol.EventObjective, Text: "task of " + session}}}, nil
		},
		Describe: func(context.Context) (protocol.DescribeResult, error) {
			return protocol.DescribeResult{Provider: "toy", Model: "any"}, nil
		},
		Host: func(_ context.Context, name string, params json.RawMessage) (protocol.HostResult, error) {
			d.mu.Lock()
			defer d.mu.Unlock()

			d.asked[session] = append(d.asked[session], name)

			if name == "branch" {
				d.points = append(d.points, string(params))

				return protocol.HostResult{Text: "session " + d.branch + " is a branch of this one", Values: []protocol.Value{{Value: d.branch, Label: "a branch"}}, State: state}, nil
			}

			return protocol.HostResult{Text: name + " of " + session, State: state}, nil
		},
		Processes: func(context.Context) (protocol.ProcessesResult, error) {
			return protocol.ProcessesResult{Processes: []protocol.ProcessUsage{{PID: int(session[len(session)-1]), Role: protocol.ProcessHost, Memory: 20 << 20, CPU: 1}}}, nil
		},
	}
}

func (d *directory) title(session string) string {
	for _, each := range d.sessions {
		if each.Value == session {
			return each.Label
		}
	}

	return ""
}

func (d *directory) info(session string) protocol.FrontendInitializeParams {
	functions := []protocol.Function{{Name: "branch", Group: protocol.GroupSession, Params: []protocol.Option{{Name: "point", Type: protocol.OptionString, Optional: true}}}, {Name: "quit", Group: protocol.GroupSystem}}

	return protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Session: session, Title: d.title(session), Provider: "toy", Model: "any", Functions: functions}
}

// among is the terminal UI started by the rumbo of the first of the
// sessions given, in a folder that holds them all, most recently used
// first.
func among(t *testing.T, sessions ...string) (*stage, *directory) {
	t.Helper()

	folder := &directory{asked: map[string][]string{}}

	for _, session := range sessions {
		folder.sessions = append(folder.sessions, protocol.Value{Value: session, Label: "title of " + session})
	}

	s := staged(t, folder.handlers(sessions[0]), folder.info(sessions[0]))

	// The sidebar starts put away.
	s.apply(ctrl('b'))

	s.a.rumbos = rumbos{
		start: func(dir, session string) (started, error) {
			if session == "broken" {
				return started{}, errors.New("session broken is not there")
			}

			conn, stop := hostedUntil(t, folder.handlers(session), folder.info(session))

			folder.mu.Lock()
			defer folder.mu.Unlock()

			folder.started = append(folder.started, dir+" "+session)

			return started{conn, folder.info(session), func() {
				stop()

				folder.mu.Lock()
				defer folder.mu.Unlock()

				folder.stopped = append(folder.stopped, session)
			}}, nil
		},
		list: func(string) ([]listing, error) {
			folder.mu.Lock()
			defer folder.mu.Unlock()

			var listed []listing

			for _, each := range folder.sessions {
				listed = append(listed, listing{session: each.Value, title: each.Label})
			}

			return listed, nil
		},
		remove: func(_, session string) error {
			folder.mu.Lock()
			defer folder.mu.Unlock()

			// A session is its rumbo's for as long as that one lives.
			if open := slices.Contains(folder.started, "/home/me/alpha "+session) && !slices.Contains(folder.stopped, session); open || session == sessions[0] {
				return errors.New("session " + session + " is in use")
			}

			folder.removed = append(folder.removed, session)
			folder.sessions = slices.DeleteFunc(folder.sessions, func(each protocol.Value) bool { return each.Value == session })

			return nil
		},
	}

	s.run(tea.Batch(s.a.list(), s.a.load(s.a.host)))

	return s, folder
}

// went shows a session, as choosing it in the palette does.
func (s *stage) went(session string) {
	s.t.Helper()
	s.run(s.a.reach(session, nil))
}

// held are the sessions the terminal UI holds a rumbo for, the first one
// first.
func (s *stage) held() string {
	var sessions []string

	for _, each := range s.a.hosts {
		sessions = append(sessions, each.session)
	}

	return strings.Join(sessions, " ")
}

// mark is what the sidebar shows beside a session, after its title.
func (s *stage) mark(title string) string {
	for _, row := range strings.Split(s.screen(), "\n") {
		side, _, _ := strings.Cut(row, "│")

		if rest, found := strings.CutPrefix(strings.TrimLeft(side, "▌ "), title); found {
			return strings.TrimSpace(rest)
		}
	}

	return "not listed"
}

// The sidebar lists the sessions of the folder. Showing another starts a
// rumbo for it, which says what it holds; going back to one that is held
// starts nothing.
func TestAnotherSessionIsARumboOfItsOwn(t *testing.T) {
	s, folder := among(t, "s1", "s2")

	if screen := s.screen(); !inOrder(screen, "+ New session", "SESSIONS", "▌ title of s1", "title of s2") || !strings.Contains(screen, "task of s1") {
		t.Fatalf("the screen:\n%s", screen)
	}

	// Tab goes to the sessions, and Enter shows the one chosen.
	s.press(tea.KeyTab)
	s.press(tea.KeyDown)
	s.press(tea.KeyEnter)

	if !slices.Equal(folder.started, []string{"/home/me/alpha s2"}) || s.held() != "s1 s2" || s.a.session != "s2" {
		t.Fatalf("started %v, holds %q, shows %q", folder.started, s.held(), s.a.session)
	}

	if screen := s.screen(); !inOrder(screen, "title of s1", "▌ title of s2") || s.mark("title of s1") != "○" || !strings.Contains(screen, "task of s2") || strings.Contains(screen, "task of s1") {
		t.Fatalf("the screen of the other session:\n%s", screen)
	}

	// A click on the first goes back to it, in the rumbo that holds it.
	s.click(4, bodyTop+sidebarSessionsRow)

	if len(folder.started) != 1 || s.a.session != "s1" || !strings.Contains(s.screen(), "task of s1") {
		t.Fatalf("started %v, shows %q:\n%s", folder.started, s.a.session, s.screen())
	}

	// A command acts on the session shown, and quitting on the rumbo that
	// started the terminal UI, whichever shows.
	s.went("s2")
	s.typing("/quit")

	if !slices.Equal(folder.asked["s1"], []string{"quit"}) || len(folder.asked["s2"]) != 0 {
		t.Fatalf("asked %v", folder.asked)
	}

	// One that does not start says why, and nothing changes.
	s.went("broken")

	if s.a.notice != "the session could not be opened: session broken is not there" || s.a.session != "s2" || s.a.opening {
		t.Fatalf("notice %q, shows %q", s.a.notice, s.a.session)
	}
}

// Ctrl-N starts a new session, a rumbo of its own, once, and the first run
// in it gives it its id, which the sidebar marks from then on.
func TestANewSessionIsARumboOfItsOwn(t *testing.T) {
	s, folder := among(t, "s1")

	s.apply(ctrl('n'))
	s.apply(ctrl('n'))

	if !slices.Equal(folder.started, []string{"/home/me/alpha "}) || s.held() != "s1 " {
		t.Fatalf("started %v, holds %q", folder.started, s.held())
	}

	if screen := s.screen(); !inOrder(screen, "▌ + New session") || !strings.Contains(screen, "Give it a task below") {
		t.Fatalf("the screen of a new session:\n%s", screen)
	}

	run := &flight{host: s.a.host, label: "write the docs", conv: s.a.shown}
	s.a.runs = append(s.a.runs, run)
	folder.sessions = append([]protocol.Value{{Value: "s9", Label: "write the docs"}}, folder.sessions...)

	s.apply(eventMsg{run, protocol.Event{Type: protocol.EventContext, Session: "s9"}})

	if screen := s.screen(); s.a.session != "s9" || !inOrder(screen, "▌ write the docs", "title of s1") {
		t.Fatalf("session %q:\n%s", s.a.session, screen)
	}

	// With that one working, another new session is another rumbo.
	s.apply(ctrl('n'))

	if len(folder.started) != 2 || s.held() != "s1 s9 " {
		t.Fatalf("started %v, holds %q", folder.started, s.held())
	}
}

// A run goes on in a session that is left: the header counts it, the
// sidebar marks it, and its end is said where the person is.
func TestARunGoesOnInTheSessionLeft(t *testing.T) {
	s, _ := among(t, "s1", "s2")

	run := &flight{host: s.a.host, label: "fix it", conv: s.a.shown}
	s.a.runs = append(s.a.runs, run)

	s.went("s2")

	if screen := s.screen(); !strings.Contains(screen, "1 working elsewhere") || s.a.here() != nil {
		t.Fatalf("the screen while a run works elsewhere:\n%s", screen)
	}

	s.apply(eventMsg{run, protocol.Event{Type: protocol.EventText, Text: "Fixed."}})
	s.apply(endedMsg{run, protocol.RunResult{Status: protocol.RunDone, Session: "s1"}, nil})

	screen := s.screen()

	if s.mark("title of s1") != "•" || !strings.Contains(screen, "the run in “title of s1” ended: done") || strings.Contains(screen, "working elsewhere") || strings.Contains(screen, "Fixed.") {
		t.Fatalf("the screen once it ended:\n%s", screen)
	}

	// Going back shows what it did, and the mark goes.
	s.went("s1")

	if screen := s.screen(); !inOrder(screen, "task of s1", "Fixed.", "✓ done") || s.mark("title of s1") != "" || s.mark("title of s2") != "○" {
		t.Fatalf("the session it ran in:\n%s", screen)
	}
}

// The rumbos of the sessions left with nothing to do are closed past the
// two shown last, never the one that started the terminal UI or one that
// works, and all of them when the person asks.
func TestRumbosLeftUnusedAreClosed(t *testing.T) {
	s, folder := among(t, "s1", "s2", "s3", "s4", "s5", "s6")

	for _, session := range []string{"s2", "s3", "s4"} {
		s.went(session)
	}

	if len(folder.stopped) != 0 || s.held() != "s1 s2 s3 s4" {
		t.Fatalf("stopped %v, holds %q", folder.stopped, s.held())
	}

	// A run works in s3 when it is left.
	working := &flight{host: s.a.holding("s3"), label: "go on", conv: s.a.holding("s3").shown}
	working.host.runs = append(working.host.runs, working)

	s.went("s5")
	s.went("s6")

	if !slices.Equal(folder.stopped, []string{"s2"}) || s.held() != "s1 s3 s4 s5 s6" {
		t.Fatalf("stopped %v, holds %q", folder.stopped, s.held())
	}

	// Its session is as it was: showing it again starts a rumbo for it.
	s.went("s2")

	if !slices.Equal(folder.stopped, []string{"s2", "s4"}) || s.held() != "s1 s3 s5 s6 s2" || !strings.Contains(s.screen(), "task of s2") {
		t.Fatalf("stopped %v, holds %q", folder.stopped, s.held())
	}

	s.typing("/rumbos close")

	if !slices.Equal(folder.stopped, []string{"s2", "s4", "s5", "s6"}) || s.held() != "s1 s3 s2" {
		t.Fatalf("stopped %v, holds %q", folder.stopped, s.held())
	}

	// One whose run ended unseen is kept until it is shown, so what it did
	// and its mark are not lost.
	closeAll := func() {
		s.typing("/rumbos close")
	}

	s.apply(endedMsg{working, protocol.RunResult{Status: protocol.RunDone, Session: "s3"}, nil})
	closeAll()

	if s.held() != "s1 s3 s2" || s.mark("title of s3") != "•" {
		t.Fatalf("holds %q, s3 is marked %q", s.held(), s.mark("title of s3"))
	}

	s.went("s3")
	s.went("s2")
	closeAll()

	if s.held() != "s1 s2" {
		t.Fatalf("holds %q once s3 was seen", s.held())
	}

	// Quitting ends the ones it started, and not the one that started it.
	s.a.shutdown()
	slices.Sort(folder.stopped)

	if !slices.Equal(folder.stopped, []string{"s2", "s2", "s3", "s4", "s5", "s6"}) {
		t.Fatalf("stopped %v", folder.stopped)
	}
}

// A branch is another session, which shows once its rumbo starts; what the
// person asked can be changed in a branch that starts before it, from the
// conversation.
func TestABranchShowsInARumboOfItsOwn(t *testing.T) {
	s, folder := among(t, "s1")
	folder.branch = "b1"
	folder.sessions = append(folder.sessions, protocol.Value{Value: "b1", Label: "a branch"})

	s.typing("/branch")

	if !slices.Equal(folder.started, []string{"/home/me/alpha b1"}) || s.a.session != "b1" {
		t.Fatalf("started %v, shows %q", folder.started, s.a.session)
	}

	if screen := s.screen(); !inOrder(screen, "▌ a branch") || !strings.Contains(screen, "task of b1") {
		t.Fatalf("the screen of the branch:\n%s", screen)
	}

	// Back in the first, Enter on what was asked offers to change it.
	s.went("s1")

	if screen := s.screen(); !inOrder(screen, "task of s1", "› /branch", "session b1 is a branch of this one") {
		t.Fatalf("the session branched:\n%s", screen)
	}

	folder.branch = "b2"
	folder.sessions = append(folder.sessions, protocol.Value{Value: "b2", Label: "another"})

	s.apply(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	s.press(tea.KeyHome)
	s.press(tea.KeyEnter)

	if screen := s.screen(); !inOrder(screen, "your message", "Change it", "Ask it again", "Copy it") {
		t.Fatalf("the menu of a message:\n%s", screen)
	}

	s.press(tea.KeyEnter)

	if s.a.session != "b2" || s.a.input.String() != "task of s1" || s.a.focus != focusComposer || !slices.Equal(folder.asked["s1"], []string{"branch", "branch"}) {
		t.Fatalf("shows %q, typed %q, asked %v", s.a.session, s.a.input.String(), folder.asked)
	}
}

// In the conversation, j and k choose among the entries, K and J among the
// person's messages, gg and G go to either end, c copies the entry chosen,
// and b chooses the message its task started with and branches from it,
// once the person agrees after being told what that keeps.
func TestTheConversationBranchesFromAnEntry(t *testing.T) {
	s, folder := among(t, "s1")
	folder.branch = "b1"
	folder.sessions = append(folder.sessions, protocol.Value{Value: "b1", Label: "a branch"})

	s.a.shown.add(&entry{kind: entryReply, text: "One."})
	s.a.shown.add(&entry{kind: entryTask, text: "second"})
	s.a.shown.add(&entry{kind: entryReply, text: "Two."})

	letter := func(text string) {
		s.apply(tea.KeyPressMsg{Code: rune(text[0]), Text: text})
	}

	chose := func(want int) {
		t.Helper()

		if s.a.focus != focusConversation || s.a.shown.selected != want {
			t.Fatalf("focus %d, chose %d, not %d", s.a.focus, s.a.shown.selected, want)
		}
	}

	s.apply(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	chose(3)

	for _, step := range []struct {
		keys string
		want int
	}{{"k", 2}, {"j", 3}, {"K", 2}, {"K", 0}, {"K", 0}, {"J", 2}, {"gg", 0}, {"G", 3}, {"gkg", 2}} {
		for _, key := range step.keys {
			letter(string(key))
		}

		chose(step.want)
	}

	letter("c")

	if !strings.Contains(s.a.notice, "copied 1 line") {
		t.Fatalf("notice %q", s.a.notice)
	}

	// From a reply, b chooses the message first, and Enter on the button
	// that has the keyboard leaves things as they are.
	letter("G")
	letter("b")
	chose(2)

	if screen := s.screen(); !inOrder(screen, "Branch from your message “second”", "Cancel", "Branch") {
		t.Fatalf("the confirmation:\n%s", screen)
	}

	s.press(tea.KeyEnter)

	if len(folder.points) != 0 || s.a.confirming != nil {
		t.Fatalf("branched %v without agreeing", folder.points)
	}

	chose(2)

	letter("k")
	letter("b")
	chose(0)
	s.press(tea.KeyRight)
	s.press(tea.KeyEnter)

	if !slices.Equal(folder.points, []string{`{"point":"1"}`}) || s.a.session != "b1" || s.a.input.String() != "task of s1" {
		t.Fatalf("branched %v, shows %q, typed %q", folder.points, s.a.session, s.a.input.String())
	}
}

// In the sidebar, j, k, gg, and G choose as in the conversation, and d
// deletes the session chosen once the person agrees: through its rumbo
// when one is held, and else from the command line.
func TestTheSidebarChoosesAndDeletes(t *testing.T) {
	s, folder := among(t, "s1", "s2", "s3")

	letter := func(text string) {
		s.apply(tea.KeyPressMsg{Code: rune(text[0]), Text: text})
	}

	s.press(tea.KeyTab)

	for _, step := range []struct {
		keys string
		want string
	}{{"j", "s2"}, {"j", "s3"}, {"k", "s2"}, {"gg", ""}, {"G", "s3"}} {
		for _, key := range step.keys {
			letter(string(key))
		}

		if s.a.focus != focusSidebar || s.a.sessions.chosen() != step.want {
			t.Fatalf("after %q: focus %d, chose %q", step.keys, s.a.focus, s.a.sessions.chosen())
		}
	}

	letter("d")

	if screen := s.screen(); !inOrder(screen, "This deletes “title of s3”", "Cancel", "Delete") {
		t.Fatalf("the confirmation:\n%s", screen)
	}

	s.press(tea.KeyRight)
	s.press(tea.KeyEnter)

	if !slices.Equal(folder.removed, []string{"s3"}) || s.a.asking || s.a.notice != "“title of s3” was deleted" {
		t.Fatalf("removed %v, asking %v, notice %q", folder.removed, s.a.asking, s.a.notice)
	}

	// The one rumbo started in is not deleted from here.
	if s.a.focus != focusSidebar {
		t.Fatalf("focus %d once deleted", s.a.focus)
	}

	s.a.sessions.choose("s1")
	letter("d")

	if s.a.confirming != nil || !strings.Contains(s.screen(), "holds it until you quit") {
		t.Fatalf("the session rumbo started in:\n%s", s.screen())
	}
}

// /rumbos says what the rumbo of each session open uses, each process
// counted once: those the terminal UI started are under the first one's
// front end, and are not counted there again, nor is what one of them
// started and did not list itself.
func TestRumbosSayWhatEachUses(t *testing.T) {
	s, _ := among(t, "s1", "s2")
	s.went("s2")

	megabyte := int64(1 << 20)

	said := []hostUsage{
		{host: s.a.first(), processes: []protocol.ProcessUsage{
			{PID: 10, Role: protocol.ProcessHost, Memory: 20 * megabyte, CPU: 1},
			{PID: 11, Parent: 10, Role: protocol.ProcessFrontend, Memory: 30 * megabyte, CPU: 2},
			{PID: 20, Parent: 11, Role: protocol.ProcessFrontend, Memory: 20 * megabyte, CPU: 3},
			{PID: 21, Parent: 20, Role: protocol.ProcessFrontend, Memory: 10 * megabyte, CPU: 0.5},
			{PID: 22, Parent: 20, Role: protocol.ProcessFrontend, Memory: megabyte, CPU: 0},
		}},
		{host: s.a.host, processes: []protocol.ProcessUsage{
			{PID: 20, Parent: 11, Role: protocol.ProcessHost, Memory: 20 * megabyte, CPU: 3},
			{PID: 21, Parent: 20, Role: protocol.ProcessProvider, Name: "toy", Memory: 10 * megabyte, CPU: 0.5},
		}},
	}

	text := s.a.rumbosText(said)

	for _, want := range []string{
		"s1       open   2          50.0 MB  3s    title of s1",
		"s2       shown  2          30.0 MB  3.5s  title of s2",
		"2 rumbos, 4 processes, at most 80.0 MB of memory, 6.5s of processor time",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("no %q in:\n%s", want, text)
		}
	}

	// The command asks every rumbo held, and shows it where it was typed.
	s.typing("/rumbos")

	if screen := s.screen(); !inOrder(screen, "› /rumbos", "SESSION", "s1", "s2", "2 rumbos, 2 processes, at most 40.0 MB") || s.a.asking {
		t.Fatalf("the screen:\n%s", screen)
	}
}

// A rumbo the terminal UI started that ends by itself leaves the first one
// showing, and says so; the terminal UI ends with the one that started it.
func TestARumboThatEndsIsLetGo(t *testing.T) {
	s, folder := among(t, "s1", "s2")
	s.went("s2")

	ended := s.a.host
	s.apply(hostGoneMsg{ended})

	if s.held() != "s1" || s.a.session != "s1" || !strings.Contains(s.a.notice, "the rumbo of “title of s2” ended") || !slices.Equal(folder.stopped, []string{"s2"}) {
		t.Fatalf("holds %q, shows %q, notice %q, stopped %v", s.held(), s.a.session, s.a.notice, folder.stopped)
	}

	_, cmd := s.a.Update(hostGoneMsg{s.a.first()})

	if quit := fmt.Sprintf("%T", messagesOf(cmd)[0]); quit != "tea.QuitMsg" {
		t.Fatalf("the first rumbo ended, and the terminal UI did %s", quit)
	}
}

// /delete deletes the session shown once the person agrees: its rumbo is
// ended first, and the first one shows in its place. The session rumbo
// started in is not one to delete from here.
func TestTheSessionShownIsDeleted(t *testing.T) {
	s, folder := among(t, "s1", "s2")

	s.typing("/delete")

	if s.a.confirming != nil || !strings.Contains(s.screen(), "rumbo sessions rm s1") {
		t.Fatalf("the screen:\n%s", s.screen())
	}

	s.went("s2")
	s.typing("/delete")

	if s.a.confirming == nil || !inOrder(s.screen(), "delete", "This deletes “title of s2” and its history, for good.", "Cancel", "Delete") {
		t.Fatalf("the screen:\n%s", s.screen())
	}

	s.press(tea.KeyEnter)

	if len(folder.removed) != 0 || s.held() != "s1 s2" || !strings.Contains(s.screen(), "nothing was done") {
		t.Fatalf("removed %v, holds %q:\n%s", folder.removed, s.held(), s.screen())
	}

	s.typing("/delete")
	s.press(tea.KeyRight)
	s.press(tea.KeyEnter)

	if !slices.Equal(folder.removed, []string{"s2"}) || s.held() != "s1" || s.a.session != "s1" || s.a.asking {
		t.Fatalf("removed %v, holds %q, shows %q", folder.removed, s.held(), s.a.session)
	}

	if screen := s.screen(); s.mark("title of s2") != "not listed" || !strings.Contains(screen, "“title of s2” was deleted") {
		t.Fatalf("the screen:\n%s", screen)
	}
}
