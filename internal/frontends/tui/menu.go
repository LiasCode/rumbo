package tui

import (
	"fmt"
	"strings"
)

// maxChoices bounds how many choices the menu shows at once.
const maxChoices = 8

// choice is one thing the menu offers: what it puts in the input, and what
// tells it apart from the others.
type choice struct {
	value       string
	label       string
	description string
}

// menu offers what the word being typed can become: a command, or a value
// of the parameter it is for, with what the host or the provider said about
// each.
type menu struct {
	// head is what comes before the word the choices are for. They are
	// worked out again when it changes.
	head    string
	choices []choice
	// shown are the choices that match token, the word being typed, and
	// selected the one Tab and Enter take.
	token    string
	shown    []choice
	selected int
	offset   int
	// loading is true while the choices are being asked for, and hint says
	// why there are none when it is not plain.
	loading bool
	hint    string
}

// filter keeps the choices that match token: those whose value starts with
// it, and those whose label holds it, ignoring case, so a session is found
// by its title as well as by its id.
func (m *menu) filter(token string) {
	lower := strings.ToLower(token)
	m.token, m.shown = token, m.shown[:0]

	for _, offered := range m.choices {
		if strings.HasPrefix(offered.value, token) || (lower != "" && strings.Contains(strings.ToLower(offered.label), lower)) {
			m.shown = append(m.shown, offered)
		}
	}

	m.selected, m.offset = 0, 0
}

// move changes the selection by step, going round at either end.
func (m *menu) move(step int) {
	if len(m.shown) == 0 {
		return
	}

	m.selected = (m.selected + step + len(m.shown)) % len(m.shown)

	switch {
	case m.selected < m.offset:
		m.offset = m.selected
	case m.selected >= m.offset+maxChoices:
		m.offset = m.selected - maxChoices + 1
	}
}

// current returns the selected choice, if any.
func (m *menu) current() (choice, bool) {
	if m.selected >= len(m.shown) {
		return choice{}, false
	}

	return m.shown[m.selected], true
}

// rows draws the menu at width columns: a row for each choice in view, the
// selected one marked, and one saying how many there are when they do not
// all fit, or why there are none.
func (m *menu) rows(width int) []string {
	switch {
	case m.loading:
		return []string{styleDim + "  asking for the values…"}
	case len(m.shown) == 0 && m.hint != "":
		return []string{styleDim + "  " + cut(m.hint, width-2)}
	case len(m.shown) == 0:
		return nil
	}

	end := min(m.offset+maxChoices, len(m.shown))
	values, labels := 0, 0

	for _, offered := range m.shown[m.offset:end] {
		values = max(values, visibleWidth(offered.value))
		labels = max(labels, visibleWidth(offered.label))
	}

	values, labels = min(values, width/3), min(labels, width/3)

	var rows []string

	for index := m.offset; index < end; index++ {
		offered := m.shown[index]
		row := "  " + pad(cut(offered.value, values), values)

		if labels > 0 {
			row += "  " + pad(cut(offered.label, labels), labels)
		}

		if offered.description != "" {
			row += "  " + styleDim + offered.description + styleReset
		}

		row = pad(cut(row, width-1), width-1)

		// The selection shows across the whole row, whatever styles its
		// parts have.
		if index == m.selected {
			row = styleReverse + "▸" + string([]rune(stripped(row))[1:]) + styleReset
		}

		rows = append(rows, row)
	}

	if len(m.shown) > maxChoices {
		rows = append(rows, styleDim+fmt.Sprintf("  %d of %d; ↑ ↓ to move, Tab to take", m.selected+1, len(m.shown)))
	}

	return rows
}
