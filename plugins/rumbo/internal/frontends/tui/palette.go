package tui

import (
	"strings"

	"github.com/LiasCode/rumbo/protocol"
)

// The command palette offers, by name, what the terminal UI can do: its own
// actions, the other sessions of the directory to show one, and every
// function the host and the provider describe, the host's by what they act
// on, as rumbo-gui's palette does. Typing narrows it.

// What a row of the palette does when it is chosen.
const (
	actionFunction = iota
	actionPicker
	actionHelp
	actionCopy
	actionExport
	actionNewSession
	actionSessions
	actionSidebar
	actionOpen
	actionRumbos
	actionCloseIdle
	// What the session shown offers, and the panels.
	actionDetails
	actionChanges
	actionRename
	actionBranch
	actionCompact
	actionCopyID
	// What can be done with a message the person wrote.
	actionEdit
	actionAskAgain
	actionCopyText
	// actionNone is a row that only says something, such as why the
	// provider's functions are not known.
	actionNone
)

// paletteEntry is one row of the palette.
type paletteEntry struct {
	group       string
	name        string
	description string
	action      int
	// function and source are the function a row calls, and whose it is;
	// session is the session a row shows.
	function protocol.Function
	source   string
	session  string
	// point, text, and images are the message a row acts on: its number
	// among the person's tasks, its words, and the images given with it.
	point  int
	text   string
	images []protocol.Image
}

// palette is the palette's rows, what is typed to narrow them, and the one
// selected. A fixed one, such as what can be done with a message, is not
// narrowed, and has a title of its own.
type palette struct {
	entries []paletteEntry
	query   inputLine
	title   string
	fixed   bool
	// shown are the entries that match what is typed, by index, and
	// selected the one Enter chooses among them; top is the first row in
	// view.
	shown    []int
	selected int
	top      int
}

func newPalette(entries []paletteEntry) *palette {
	p := &palette{entries: entries}
	p.narrow()

	return p
}

// narrow keeps the entries whose name or description holds what is typed,
// ignoring case, and selects the first whose name starts with it, which is
// the one being typed, else the first.
func (p *palette) narrow() {
	query := strings.ToLower(strings.TrimSpace(p.query.String()))
	p.shown, p.selected, p.top = p.shown[:0], 0, 0

	starts := -1

	for index, each := range p.entries {
		name := strings.ToLower(each.name)

		if query != "" && !strings.Contains(name, query) && !strings.Contains(strings.ToLower(each.description), query) {
			continue
		}

		if starts < 0 && query != "" && strings.HasPrefix(name, query) && each.action != actionNone {
			starts = len(p.shown)
		}

		p.shown = append(p.shown, index)
	}

	if starts >= 0 {
		p.selected = starts
	}
}

// chosen is the entry selected, if any.
func (p *palette) chosen() (paletteEntry, bool) {
	if p.selected >= len(p.shown) {
		return paletteEntry{}, false
	}

	return p.entries[p.shown[p.selected]], true
}

// move changes the selection by step, going round at either end.
func (p *palette) move(step int) {
	if len(p.shown) > 0 {
		p.selected = (p.selected + step + len(p.shown)) % len(p.shown)
	}
}

// rows draws the entries that match in rows of width columns, at most
// height of them: each group's name over its entries, the one selected in
// reverse, and the view scrolled to keep it in sight.
func (p *palette) rows(width, height int) []string {
	if len(p.shown) == 0 {
		return []string{styleDim + "nothing matches" + styleReset}
	}

	var rows []string

	at, group := 0, ""

	for position, index := range p.shown {
		each := p.entries[index]

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

		rows = append(rows, paletteRow(each, width, position == p.selected))
	}

	height = max(height, 1)

	switch {
	case at < p.top:
		p.top = at
	case at >= p.top+height:
		p.top = at - height + 1
	}

	// A group's name shows with its first entry.
	if p.top > 0 && p.top == at && rows[at-1] != "" {
		p.top--
	}

	p.top = max(min(p.top, len(rows)-height), 0)

	return rows[p.top:min(p.top+height, len(rows))]
}

// paletteRow draws one entry: its name, and what it does after it, dimmed.
func paletteRow(each paletteEntry, width int, selected bool) string {
	names := min(28, width/2)
	name := pad(cut(each.name, names), names)

	if each.action == actionNone {
		return styleDim + cut("  "+each.name, width) + styleReset
	}

	row := "  " + name + "  " + styleDim + each.description + styleReset

	if selected {
		return styleReverse + "▸ " + pad(string([]rune(stripped(cut(row, width)))[2:]), width-2) + styleReset
	}

	return cut(row, width)
}
