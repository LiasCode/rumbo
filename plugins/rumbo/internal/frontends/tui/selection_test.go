package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/LiasCode/rumbo/protocol"
)

// Dragging the pointer over the conversation chooses its text, shown in
// reverse, and letting the button go copies it, to the clipboard and to
// the primary selection; a drag that leaves the conversation stays in it.
// The conversation holds still meanwhile, and follows what comes again
// once the selection goes, with the next key.
func TestTextDraggedOverIsChosenAndCopied(t *testing.T) {
	s := composing(t)
	s.a.shown.ask("say it", nil, false, s.a.now)
	s.a.shown.apply(protocol.Event{Type: protocol.EventText, Text: "the first line\nthe second line\n"}, false, s.a.now)
	s.a.shown.end(protocol.RunDone, "", s.a.now)

	rows := strings.Split(s.screen(), "\n")

	find := func(text string) point {
		for y, row := range rows {
			if x := strings.Index(row, text); x >= 0 {
				return point{x: len([]rune(row[:x])), y: y}
			}
		}

		t.Fatalf("%q is not on the screen:\n%s", text, s.screen())

		return point{}
	}

	from, to := find("first line"), find("second line")
	to.x += len("second") - 1

	s.apply(tea.MouseClickMsg{X: from.x, Y: from.y, Button: tea.MouseLeft})
	s.apply(tea.MouseMotionMsg{X: to.x, Y: to.y, Button: tea.MouseLeft})

	if !strings.Contains(s.a.View().Content, styleReverse+"first line") || s.a.shown.follow {
		t.Fatalf("the selection is not shown, or the conversation still follows: %+v", s.a.selection)
	}

	_, cmd := s.a.Update(tea.MouseReleaseMsg{X: to.x, Y: to.y, Button: tea.MouseLeft})

	var copied []string

	for _, msg := range messagesOf(cmd) {
		copied = append(copied, fmt.Sprint(msg))
	}

	if want := "first line\nthe second"; len(copied) != 2 || copied[0] != want || copied[1] != want || !strings.Contains(s.screen(), "copied 2 line(s)") || strings.Contains(s.screen(), "more below") {
		t.Fatalf("copied %q:\n%s", copied, s.screen())
	}

	// A drag past the conversation stops at its edge, and takes
	// the cell it started on, as a terminal does.
	s.apply(tea.MouseClickMsg{X: from.x, Y: from.y, Button: tea.MouseLeft})
	s.apply(tea.MouseMotionMsg{X: 0, Y: from.y, Button: tea.MouseLeft})

	if text := s.a.selection.text(s.a.screenRows(s.a.layout())); text != "the f" {
		t.Fatalf("chose %q", text)
	}

	s.apply(tea.MouseReleaseMsg{X: 0, Y: from.y, Button: tea.MouseLeft})
	s.text("x")

	if s.a.selection != nil || !s.a.shown.follow || strings.Contains(s.a.View().Content, styleReverse+"the f") {
		t.Fatalf("after a key: selection %+v, follow %v", s.a.selection, s.a.shown.follow)
	}
}

// A press that does not move is a click, which acts once the button comes
// up, as on a tool call, which shows all of itself.
func TestAPressThatDoesNotMoveIsAClick(t *testing.T) {
	s := composing(t)
	s.a.shown.apply(protocol.Event{Type: protocol.EventToolStarted, Name: "bash", Args: []byte(`{"command":"ls"}`)}, false, s.a.now)
	s.a.shown.apply(protocol.Event{Type: protocol.EventToolFinished, Name: "bash", Result: "a.txt"}, false, s.a.now)

	rows := strings.Split(s.screen(), "\n")

	for y, row := range rows {
		if x := strings.Index(row, "bash ls"); x >= 0 {
			s.click(len([]rune(row[:x])), y)
			break
		}
	}

	if s.a.selection != nil || !s.a.shown.entries[0].expanded {
		t.Fatalf("the click chose %+v, and the call is expanded: %v", s.a.selection, s.a.shown.entries[0].expanded)
	}
}
