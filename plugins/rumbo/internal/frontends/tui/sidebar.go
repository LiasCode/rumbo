package tui

import (
	"fmt"
	"time"

	"github.com/LiasCode/rumbo/internal/frontends/render"
)

// sidebar lists the sessions of the directory as rumbo sessions lists them,
// most recently used first, under an entry that starts a new one, as
// rumbo-gui's sidebar lists a project's. Each is a rumbo of its own, which
// the terminal UI starts when the session is first shown.
type sidebar struct {
	listed []listing
	// selected is what Enter opens while the sidebar has the keyboard: 0
	// is a new session, and each session is one more than its place in
	// listed. top is the first session in view.
	selected int
	top      int
}

// The rows of the sidebar before its sessions: the project, a new session,
// and the heading of the sessions. Each session takes sidebarStride rows:
// its own, and one that keeps it apart from the next.
const (
	sidebarNewRow      = 2
	sidebarSessionsRow = 5
	sidebarStride      = 2
)

// listing is a session as rumbo sessions lists it: its id, what it is
// called, how its last run stands, and when it was last used.
type listing struct {
	session string
	title   string
	state   string
	used    time.Time
}

// marks are what the sidebar shows of each session besides its title: the
// one shown, those working, those whose run ended while they were not
// shown, and those a rumbo of this terminal UI is still open for.
type marks struct {
	current string
	working map[string]bool
	unseen  map[string]bool
	open    map[string]bool
}

// list takes the sessions as rumbo lists them, keeping the selection on
// the session it was on.
func (s *sidebar) list(values []listing) {
	chosen := s.chosen()
	s.listed = values
	s.selected = 0

	for index, listed := range values {
		if chosen != "" && listed.session == chosen {
			s.selected = index + 1
		}
	}
}

// chosen is the session selected, or nothing for a new one.
func (s *sidebar) chosen() string {
	if s.selected == 0 || s.selected > len(s.listed) {
		return ""
	}

	return s.listed[s.selected-1].session
}

// choose selects a session, or a new one for nothing.
func (s *sidebar) choose(session string) {
	s.selected = 0

	for index, listed := range s.listed {
		if listed.session == session {
			s.selected = index + 1
		}
	}
}

// move changes the selection by step, stopping at either end, and keeps it
// in view in a sidebar height rows high.
func (s *sidebar) move(step, height int) {
	s.selected = max(min(s.selected+step, len(s.listed)), 0)
	s.reveal(height)
}

// room is how many sessions are in view in a sidebar height rows high.
func (s *sidebar) room(height int) int {
	return max((height-sidebarSessionsRow+sidebarStride-1)/sidebarStride, 1)
}

// scroll moves the sessions in view by step, in a sidebar height rows high.
func (s *sidebar) scroll(step, height int) {
	s.top = max(min(s.top+step, len(s.listed)-s.room(height)), 0)
}

// reveal scrolls the sessions so the one selected is in view.
func (s *sidebar) reveal(height int) {
	room := s.room(height)

	switch {
	case s.selected == 0:
	case s.selected-1 < s.top:
		s.top = s.selected - 1
	case s.selected-1 >= s.top+room:
		s.top = s.selected - room
	}

	s.top = max(min(s.top, len(s.listed)-room), 0)
}

// at returns what is on a row of the sidebar, as selected counts it.
func (s *sidebar) at(row int) (int, bool) {
	switch {
	case row == sidebarNewRow:
		return 0, true
	// The row that keeps a session apart from the next is the session's
	// too, so a click there is not lost.
	case row >= sidebarSessionsRow && s.top+(row-sidebarSessionsRow)/sidebarStride < len(s.listed):
		return s.top + (row-sidebarSessionsRow)/sidebarStride + 1, true
	}

	return 0, false
}

// rows draws the sidebar width columns wide and height rows high: the
// project, the entry for a new session, and the sessions, the one shown
// marked with a bar, each working one with a spinner, each that ended
// unseen with a dot, and each other one a rumbo is open for with a ring,
// each with how long ago it was used. While it has the keyboard, the one
// selected shows in reverse.
func (s *sidebar) rows(t theme, project string, width, height int, shown marks, focused bool, now time.Time) []string {
	rows := make([]string, 0, height)

	entry := func(index int, title, after, mark string, current bool) string {
		bar := " "

		if current {
			bar = t.progress + "▌" + styleColorOff
		}

		dim := ""

		if after != "" {
			after += " "
			dim = styleDim + after + styleWeightOff
		}

		room := max(width-3-visibleWidth(after)-visibleWidth(mark), 1)
		row := bar + " " + pad(cut(title, room), room) + " " + dim + mark

		if current {
			row = bar + " " + styleBold + pad(cut(title, room), room) + styleWeightOff + " " + dim + mark
		}

		if focused && index == s.selected {
			// Without its styles, since one that ends would end the reverse.
			row = styleReverse + stripped(row) + styleReset
		}

		return row
	}

	rows = append(rows, " "+styleBold+cut(project, width-2)+styleReset, "")
	rows = append(rows, entry(0, "+ New session", "", styleDim+"^N"+styleReset, shown.current == ""))
	rows = append(rows, "")

	heading := "SESSIONS"

	if len(s.listed) == 0 {
		heading = "no sessions yet"
	}

	rows = append(rows, " "+styleDim+heading+styleReset)

	for index := s.top; index < len(s.listed) && len(rows) < height; index++ {
		session := s.listed[index].session
		mark := " "

		switch {
		case shown.working[session]:
			mark = t.progress + spin(now) + styleColorOff
		case shown.unseen[session]:
			mark = t.warning + "•" + styleColorOff
		case shown.open[session] && session != shown.current:
			mark = styleDim + "○" + styleWeightOff
		}

		title := s.listed[index].title

		if title == "" {
			title = session
		}

		if index > s.top {
			rows = append(rows, "")
		}

		rows = append(rows, entry(index+1, render.FirstLine(title), ago(s.listed[index].used, now), mark, session == shown.current))
	}

	for len(rows) < height {
		rows = append(rows, "")
	}

	return rows[:height]
}

// ago says how long before now a session was used, in a few columns: its
// minutes, hours, days, or weeks, and its date past a month.
func ago(used, now time.Time) string {
	since := now.Sub(used)

	switch {
	case used.IsZero():
		return ""
	case since < time.Minute:
		return "now"
	case since < time.Hour:
		return fmt.Sprintf("%dm", int(since/time.Minute))
	case since < 24*time.Hour:
		return fmt.Sprintf("%dh", int(since/time.Hour))
	case since < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(since/(24*time.Hour)))
	case since < 30*24*time.Hour:
		return fmt.Sprintf("%dw", int(since/(7*24*time.Hour)))
	case used.Year() == now.Year():
		return used.Format("Jan 2")
	}

	return used.Format("Jan 2006")
}
