package tui

import (
	"strings"
	"testing"
	"time"
)

// The sidebar keeps the session selected when they are listed again in
// another order, and says which session each of its rows is.
func TestTheSidebarKeepsItsSelection(t *testing.T) {
	var s sidebar

	s.list([]listing{{session: "a1", title: "fix the test"}, {session: "b2"}})
	s.choose("b2")
	s.list([]listing{{session: "c3", title: "new one"}, {session: "a1", title: "fix the test"}, {session: "b2"}})

	if s.chosen() != "b2" || s.selected != 3 {
		t.Fatalf("chosen %q at %d", s.chosen(), s.selected)
	}

	// Each session takes its row and the one that keeps it apart.
	for row, want := range map[int]int{sidebarNewRow: 0, sidebarSessionsRow: 1, sidebarSessionsRow + 2: 2, sidebarSessionsRow + 4: 3, sidebarSessionsRow + 5: 3} {
		if got, ok := s.at(row); !ok || got != want {
			t.Errorf("row %d is %d, %v; want %d", row, got, ok, want)
		}
	}

	if _, ok := s.at(sidebarSessionsRow + 6); ok {
		t.Fatal("a row under the sessions is one")
	}
}

// The sidebar marks the session shown, those working, and those that ended
// unseen, says how long ago each was used, keeps them a row apart, and
// scrolls to keep the one selected in view.
func TestTheSidebarMarksAndScrolls(t *testing.T) {
	var s sidebar

	var listed []listing

	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	for index, id := range []string{"s1", "s2", "s3", "s4", "s5", "s6"} {
		listed = append(listed, listing{session: id, title: "task " + id, used: now.Add(-time.Duration(index*5) * time.Hour)})
	}

	s.list(listed)

	height := sidebarSessionsRow + 5
	rows := s.rows(defaultTheme, "project", 24, height, marks{current: "s1", working: map[string]bool{"s2": true}, unseen: map[string]bool{"s3": true}}, false, now)

	if len(rows) != height || !strings.HasPrefix(stripped(rows[sidebarSessionsRow]), "▌ task s1") || !strings.HasSuffix(strings.TrimSpace(stripped(rows[sidebarSessionsRow])), "now") ||
		strings.TrimSpace(rows[sidebarSessionsRow+1]) != "" || !strings.HasSuffix(stripped(rows[sidebarSessionsRow+4]), "10h •") {
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

// How long ago a session was used fits in a few columns.
func TestAgoIsShort(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	for took, want := range map[time.Duration]string{
		10 * time.Second:     "now",
		42 * time.Minute:     "42m",
		3 * time.Hour:        "3h",
		50 * time.Hour:       "2d",
		15 * 24 * time.Hour:  "2w",
		40 * 24 * time.Hour:  "Oct 16",
		400 * 24 * time.Hour: "Oct 2025",
	} {
		if got := ago(now.Add(-took), now); got != want {
			t.Errorf("%s ago is %q, not %q", took, got, want)
		}
	}

	if ago(time.Time{}, now) != "" {
		t.Error("an unknown time says something")
	}
}
