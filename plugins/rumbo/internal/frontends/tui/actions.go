package tui

import "strings"

// The menu of actions floats over the screen with what can be done with
// one of the person's messages: change it, ask it again, or copy it.

// What a row of the menu does when it is chosen.
const (
	actionEdit = iota
	actionAskAgain
	actionCopyText
)

// action is one row of the menu.
type action struct {
	group       string
	name        string
	description string
	action      int
	// point, text, and files are the message a row acts on: its number
	// among the person's tasks, its words, and the files given with it.
	point int
	text  string
	files []string
}

// actions are the menu's rows, under a title, and the one selected.
type actions struct {
	entries  []action
	title    string
	selected int
	top      int
}

// chosen is the row selected, if any.
func (p *actions) chosen() (action, bool) {
	if p.selected >= len(p.entries) {
		return action{}, false
	}

	return p.entries[p.selected], true
}

// move changes the selection by step, going round at either end.
func (p *actions) move(step int) {
	if len(p.entries) > 0 {
		p.selected = (p.selected + step + len(p.entries)) % len(p.entries)
	}
}

// rows draws the rows in width columns, at most height of them: each
// group's name over its rows, the one selected in reverse, and the view
// scrolled to keep it in sight.
func (p *actions) rows(width, height int) []string {
	var rows []string

	at, group := 0, ""

	for position, each := range p.entries {
		if each.group != group || position == 0 {
			if position > 0 {
				rows = append(rows, "")
			}

			rows = append(rows, styleDim+strings.ToUpper(each.group)+styleReset)
			group = each.group
		}

		if position == p.selected {
			at = len(rows)
		}

		rows = append(rows, actionRow(each, width, position == p.selected))
	}

	height = max(height, 1)

	switch {
	case at < p.top:
		p.top = at
	case at >= p.top+height:
		p.top = at - height + 1
	}

	// A group's name shows with its first row.
	if p.top > 0 && p.top == at && rows[at-1] != "" {
		p.top--
	}

	p.top = max(min(p.top, len(rows)-height), 0)

	return rows[p.top:min(p.top+height, len(rows))]
}

// actionRow draws one row: its name, and what it does after it, dimmed.
func actionRow(each action, width int, selected bool) string {
	names := min(28, width/2)
	row := "  " + pad(cut(each.name, names), names) + "  " + styleDim + each.description + styleReset

	if selected {
		return styleReverse + "▸ " + pad(string([]rune(stripped(cut(row, width)))[2:]), width-2) + styleReset
	}

	return cut(row, width)
}
