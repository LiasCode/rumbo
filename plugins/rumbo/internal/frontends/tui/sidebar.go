package tui

import (
	"time"

	"github.com/LiasCode/rumbo/internal/frontends/render"
	"github.com/LiasCode/rumbo/protocol"
)

// sidebar lists the project's sessions as the host's sessions function
// lists them, most recently used first, under an entry that starts a new
// one, as rumbo-gui's sidebar lists a project's.
type sidebar struct {
	listed []protocol.Value
	// selected is what Enter opens while the sidebar has the keyboard: 0
	// is a new session, and each session is one more than its place in
	// listed. top is the first session in view.
	selected int
	top      int
}

// The rows of the sidebar before its sessions: the project, a new session,
// and the heading of the sessions.
const (
	sidebarNewRow      = 2
	sidebarSessionsRow = 5
)

// marks are what the sidebar shows of each session besides its title: the
// one shown, those working, and those whose run ended while they were not
// shown.
type marks struct {
	current string
	working map[string]bool
	unseen  map[string]bool
}

// title is what the sidebar calls a session: its title, or its id when it
// has none.
func (s *sidebar) title(session string) string {
	for _, listed := range s.listed {
		if listed.Value == session && listed.Label != "" {
			return render.FirstLine(listed.Label)
		}
	}

	return session
}

// list takes the sessions as the host lists them, keeping the selection on
// the session it was on.
func (s *sidebar) list(values []protocol.Value) {
	chosen := s.chosen()
	s.listed = values
	s.selected = 0

	for index, listed := range values {
		if chosen != "" && listed.Value == chosen {
			s.selected = index + 1
		}
	}
}

// chosen is the session selected, or nothing for a new one.
func (s *sidebar) chosen() string {
	if s.selected == 0 || s.selected > len(s.listed) {
		return ""
	}

	return s.listed[s.selected-1].Value
}

// choose selects a session, or a new one for nothing.
func (s *sidebar) choose(session string) {
	s.selected = 0

	for index, listed := range s.listed {
		if listed.Value == session {
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

// reveal scrolls the sessions so the one selected is in view.
func (s *sidebar) reveal(height int) {
	room := max(height-sidebarSessionsRow, 1)

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
	case row >= sidebarSessionsRow && s.top+row-sidebarSessionsRow < len(s.listed):
		return s.top + row - sidebarSessionsRow + 1, true
	}

	return 0, false
}

// rows draws the sidebar width columns wide and height rows high: the
// project, the entry for a new session, and the sessions, the one shown
// marked with a bar, each working one with a spinner, and each that ended
// unseen with a dot. While it has the keyboard, the one selected shows in
// reverse.
func (s *sidebar) rows(t theme, project string, width, height int, shown marks, focused bool, now time.Time) []string {
	rows := make([]string, 0, height)

	entry := func(index int, title, mark string, current bool) string {
		bar := " "

		if current {
			bar = t.progress + "▌" + styleColorOff
		}

		room := max(width-3-visibleWidth(mark), 1)
		row := bar + " " + pad(cut(title, room), room) + " " + mark

		if current {
			row = bar + " " + styleBold + pad(cut(title, room), room) + styleWeightOff + " " + mark
		}

		if focused && index == s.selected {
			row = styleReverse + bar + " " + pad(cut(title, room), room) + " " + stripped(mark) + styleReset
		}

		return row
	}

	rows = append(rows, " "+styleBold+cut(project, width-2)+styleReset, "")
	rows = append(rows, entry(0, "+ New session", styleDim+"^N"+styleReset, shown.current == ""))
	rows = append(rows, "")

	heading := "SESSIONS"

	if len(s.listed) == 0 {
		heading = "no sessions yet"
	}

	rows = append(rows, " "+styleDim+heading+styleReset)

	for index := s.top; index < len(s.listed) && len(rows) < height; index++ {
		session := s.listed[index].Value
		mark := " "

		switch {
		case shown.working[session]:
			mark = t.progress + spin(now) + styleColorOff
		case shown.unseen[session]:
			mark = t.warning + "•" + styleColorOff
		}

		title := s.listed[index].Label

		if title == "" {
			title = session
		}

		rows = append(rows, entry(index+1, render.FirstLine(title), mark, session == shown.current))
	}

	for len(rows) < height {
		rows = append(rows, "")
	}

	return rows[:height]
}
