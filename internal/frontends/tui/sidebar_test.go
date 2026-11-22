package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/LiasCode/rumbo/protocol"
)

// The sidebar keeps the session selected when the host lists them again in
// another order, and says which session each of its rows is.
func TestTheSidebarKeepsItsSelection(t *testing.T) {
	var s sidebar

	s.list([]protocol.Value{{Value: "a1", Label: "fix the test"}, {Value: "b2"}})
	s.choose("b2")
	s.list([]protocol.Value{{Value: "c3", Label: "new one"}, {Value: "a1", Label: "fix the test"}, {Value: "b2"}})

	if s.chosen() != "b2" || s.selected != 3 {
		t.Fatalf("chosen %q at %d", s.chosen(), s.selected)
	}

	if s.title("a1") != "fix the test" || s.title("b2") != "b2" {
		t.Fatalf("titles %q and %q", s.title("a1"), s.title("b2"))
	}

	for row, want := range map[int]int{sidebarNewRow: 0, sidebarSessionsRow: 1, sidebarSessionsRow + 2: 3} {
		if got, ok := s.at(row); !ok || got != want {
			t.Errorf("row %d is %d, %v; want %d", row, got, ok, want)
		}
	}

	if _, ok := s.at(sidebarSessionsRow + 3); ok {
		t.Fatal("a row under the sessions is one")
	}
}

// The sidebar marks the session shown, those working, and those that ended
// unseen, and scrolls to keep the one selected in view.
func TestTheSidebarMarksAndScrolls(t *testing.T) {
	var s sidebar

	var listed []protocol.Value

	for _, id := range []string{"s1", "s2", "s3", "s4", "s5", "s6"} {
		listed = append(listed, protocol.Value{Value: id, Label: "task " + id})
	}

	s.list(listed)

	height := sidebarSessionsRow + 3
	rows := s.rows(defaultTheme, "project", 24, height, marks{current: "s1", working: map[string]bool{"s2": true}, unseen: map[string]bool{"s3": true}}, false, time.Now())

	if len(rows) != height || !strings.HasPrefix(stripped(rows[sidebarSessionsRow]), "▌ task s1") || !strings.HasSuffix(stripped(rows[sidebarSessionsRow+2]), "•") {
		t.Fatalf("rows:\n%s", strings.Join(rows, "\n"))
	}

	for _, row := range rows {
		if visibleWidth(row) > 24 {
			t.Fatalf("a row is wider than the sidebar: %q", stripped(row))
		}
	}

	s.move(5, height)

	if s.chosen() != "s5" || s.top != 2 {
		t.Fatalf("chosen %q, top %d", s.chosen(), s.top)
	}
}
