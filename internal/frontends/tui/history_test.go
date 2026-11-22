package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/LiasCode/rumbo/internal/frontend"
	"github.com/LiasCode/rumbo/protocol"
)

// composing is a stage whose host answers nothing, for what happens in the
// composer.
func composing(t *testing.T) *stage {
	return staged(t, frontend.Handlers{}, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any"})
}

// Up goes back through what was entered, Down comes forward again, and past
// the newest line is what was being typed before going back.
func TestTheArrowsBringBackWhatWasEntered(t *testing.T) {
	s := composing(t)

	for _, line := range []string{"fix the test", "/status", "/status", "add a flag"} {
		s.a.history.add(line)
	}

	s.a.input.set("half typ")

	var shown []string

	for _, code := range []rune{tea.KeyUp, tea.KeyUp, tea.KeyUp, tea.KeyUp, tea.KeyDown, tea.KeyDown, tea.KeyDown, tea.KeyDown} {
		s.press(code)
		shown = append(shown, s.a.input.String())
	}

	// A line entered twice in a row is kept once, and going further back
	// than the oldest line, or forward from the draft, stays where it is.
	want := []string{"add a flag", "/status", "fix the test", "fix the test", "/status", "add a flag", "half typ", "half typ"}

	if strings.Join(shown, " | ") != strings.Join(want, " | ") {
		t.Fatalf("shown %q, want %q", shown, want)
	}
}

// What the host recalls comes before what was entered here, the newest
// first, a line of several rows as one entry, which Up goes through before
// going further back.
func TestWhatWasAskedBeforeComesFirst(t *testing.T) {
	s := composing(t)

	s.apply(recalledMsg{[]string{"explain this\nand that", "fix the parser"}})
	s.a.history.add("/status")

	var shown []string

	for range 5 {
		s.press(tea.KeyUp)
		shown = append(shown, s.a.input.String())
	}

	want := []string{"/status", "explain this\nand that", "explain this\nand that", "fix the parser", "fix the parser"}

	if strings.Join(shown, " | ") != strings.Join(want, " | ") {
		t.Fatalf("shown %q, want %q", shown, want)
	}
}

// Ctrl-R looks back for what is typed, further back with Ctrl-R again;
// Enter takes the entry into the composer without sending it, and Escape
// leaves the composer as it was.
func TestCtrlRSearchesWhatWasEntered(t *testing.T) {
	s := composing(t)

	for _, line := range []string{"fix the parser", "write the docs", "fix the flags"} {
		s.a.history.add(line)
	}

	s.a.input.set("draft")
	s.apply(ctrl('r'))
	s.text("fix")

	if s.a.input.String() != "fix the flags" || !strings.Contains(s.screen(), "search: fix") {
		t.Fatalf("first found: %q", s.a.input.String())
	}

	s.apply(ctrl('r'))

	if s.a.input.String() != "fix the parser" {
		t.Fatalf("found further back: %q", s.a.input.String())
	}

	s.press(tea.KeyEnter)

	if s.a.searching || s.a.input.String() != "fix the parser" || len(s.a.runs) != 0 {
		t.Fatalf("after Enter: searching %v, composer %q", s.a.searching, s.a.input.String())
	}

	s.a.input.set("draft")
	s.apply(ctrl('r'))
	s.text("docs")
	s.press(tea.KeyEscape)

	if s.a.searching || s.a.input.String() != "draft" {
		t.Fatalf("after Escape: searching %v, composer %q", s.a.searching, s.a.input.String())
	}
}

// The keys edit where the cursor is, as readline's do, and Alt-Enter or
// Ctrl-J breaks the line instead of sending it.
func TestTheKeysEditWhereTheCursorIs(t *testing.T) {
	s := composing(t)
	s.text("fix test")

	for range 4 {
		s.press(tea.KeyLeft)
	}

	s.text("the ")

	if got := shown(&s.a.input); got != "fix the |test" {
		t.Fatalf("after inserting: %q", got)
	}

	s.apply(ctrl('a'))
	s.apply(ctrl('d')) // Ctrl-D deletes under the cursor in a composer with text.
	s.apply(ctrl('e'))
	s.apply(ctrl('w'))

	if got := shown(&s.a.input); got != "ix the |" {
		t.Fatalf("after deleting: %q", got)
	}

	s.apply(tea.KeyPressMsg{Code: 'b', Mod: tea.ModAlt})
	s.apply(ctrl('k'))

	if got := shown(&s.a.input); got != "ix |" {
		t.Fatalf("after killing to the end: %q", got)
	}

	s.apply(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	s.apply(ctrl('j'))
	s.text("more")

	if got := shown(&s.a.input); got != "ix \n\nmore|" || len(s.a.runs) != 0 {
		t.Fatalf("after breaking the line: %q", got)
	}
}
