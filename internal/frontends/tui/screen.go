package tui

import (
	"fmt"
	"io"
	"strings"
	"unicode"
)

// screen draws inline, below whatever the terminal showed before: what is
// done is written once and scrolls into the terminal's own scrollback, and
// the live area under it, which shows what is still changing, is drawn again
// in place whenever it changes. Nothing takes over the whole screen, so the
// conversation stays in the terminal like the output of any command.
type screen struct {
	out    io.Writer
	width  int
	height int
	// drawn are the rows of the live area as they were last drawn, row the
	// terminal row of them the cursor was left on, which is where drawing
	// starts again from, and span how many terminal rows they take.
	drawn []string
	row   int
	span  int
}

// Terminal control sequences the screen uses.
const (
	clearLine   = escape + "[2K"
	syncStart   = escape + "[?2026h"
	syncEnd     = escape + "[?2026l"
	cursorUp    = escape + "[%dA"
	cursorDown  = escape + "[B"
	cursorRight = escape + "[%dC"
)

// paint writes done, which goes into the scrollback for good, and then the
// live area as rows, leaving the cursor on the given row and column of it.
// done is whole lines, each ending in a line break. A row must fit in the
// screen's width, so it takes one row of the terminal; the rows are cut to
// the screen's height, keeping the last ones.
func (s *screen) paint(done string, rows []string, row, column int) {
	if limit := max(s.height-1, 1); len(rows) > limit {
		cut := len(rows) - limit
		rows, row = rows[cut:], max(row-cut, 0)
	}

	var out strings.Builder

	// What is drawn at once shows at once, in terminals that can.
	out.WriteString(syncStart)
	out.WriteString(s.erase())
	out.WriteString(strings.ReplaceAll(done, "\n", "\r\n"))

	for index, line := range rows {
		if index > 0 {
			out.WriteString("\r\n")
		}

		out.WriteString(line + styleReset)
	}

	if up := len(rows) - 1 - row; up > 0 {
		fmt.Fprintf(&out, cursorUp, up)
	}

	out.WriteString("\r")

	if column > 0 {
		fmt.Fprintf(&out, cursorRight, column)
	}

	out.WriteString(syncEnd)

	fmt.Fprint(s.out, out.String())

	s.drawn, s.row, s.span = rows, row, len(rows)
}

// top returns the sequence that takes the cursor back to where the live
// area starts.
func (s *screen) top() string {
	if s.row == 0 {
		return "\r"
	}

	return "\r" + fmt.Sprintf(cursorUp, s.row)
}

// erase takes the cursor back to where the live area starts and clears the
// rows it takes, one at a time, moving down a way that never scrolls.
// Clearing to the end of the screen at once would, from its first row,
// clear the whole screen, which some terminals, tmux among them, first move
// into their scrollback, where every drawing of the live area would stay.
func (s *screen) erase() string {
	var out strings.Builder

	out.WriteString(s.top())

	for index := range s.span {
		if index > 0 {
			out.WriteString(cursorDown)
		}

		out.WriteString(clearLine)
	}

	if s.span > 1 {
		fmt.Fprintf(&out, cursorUp, s.span-1)
	}

	return out.String()
}

// clear takes the live area away, leaving the cursor where it started, so
// the terminal goes on below the conversation.
func (s *screen) clear() {
	fmt.Fprint(s.out, s.erase())
	s.drawn, s.row, s.span = nil, 0, 0
}

// fresh moves what the terminal shows into its scrollback, as clearing the
// screen of a shell does, and starts drawing again at the top.
func (s *screen) fresh() {
	fmt.Fprint(s.out, s.erase()+strings.Repeat("\r\n", max(s.height-1, 0))+escape+"[H")
	s.drawn, s.row, s.span = nil, 0, 0
}

// resized takes the new size. A terminal that wraps its lines again when it
// narrows makes each row drawn above the cursor take more than one, so they
// are counted at the new width, to find where the live area starts.
func (s *screen) resized(width, height int) {
	if width < s.width {
		above, span := 0, 0

		for index, line := range s.drawn {
			taken := max((visibleWidth(line)+width-1)/width, 1)
			span += taken

			if index <= s.row {
				above += taken
			}
		}

		s.row, s.span = max(above-1, 0), span
	}

	s.width, s.height = width, height
}

// visibleWidth is how many columns text takes, without its escape
// sequences.
func visibleWidth(text string) int {
	columns := 0

	scan(text, func(r rune, escaped bool) bool {
		if !escaped {
			columns += runeWidth(r)
		}

		return true
	})

	return columns
}

// stripped is text without its escape sequences: what it shows.
func stripped(text string) string {
	var out strings.Builder

	scan(text, func(r rune, escaped bool) bool {
		if !escaped {
			out.WriteRune(r)
		}

		return true
	})

	return out.String()
}

// cut shortens text to width columns, keeping its escape sequences, and
// marks what it took away with an ellipsis.
func cut(text string, width int) string {
	if visibleWidth(text) <= width {
		return text
	}

	var out strings.Builder

	columns := 0

	scan(text, func(r rune, escaped bool) bool {
		if !escaped {
			if columns+runeWidth(r) > width-1 {
				return false
			}

			columns += runeWidth(r)
		}

		out.WriteRune(r)

		return true
	})

	return out.String() + "…" + styleReset
}

// scan calls each with every rune of text, saying whether it is part of an
// escape sequence, until each returns false. A sequence is an escape and
// one character, or an escape, a bracket, and what follows up to a letter.
func scan(text string, each func(r rune, escaped bool) bool) {
	const (
		plain = iota
		escaped
		bracketed
	)

	state := plain

	for _, r := range text {
		inside := state != plain

		switch {
		case state == escaped && r == '[':
			state = bracketed
		case state == escaped:
			state = plain
		case state == bracketed && r >= 0x40 && r <= 0x7e:
			state = plain
		case state == plain && r == 0x1b:
			state, inside = escaped, true
		}

		if !each(r, inside) {
			return
		}
	}
}

// pad fills text with spaces to width columns.
func pad(text string, width int) string {
	return text + strings.Repeat(" ", max(width-visibleWidth(text), 0))
}

// wrap breaks text into rows of at most width columns, at spaces when it
// can. Escape sequences take no room, and a row that goes on with a style
// begun on the row before starts with that style again.
func wrap(text string, width int) []string {
	width = max(width, 1)

	var rows []string

	for _, line := range strings.Split(text, "\n") {
		cells, trailing := cellsOf(line)
		styles := ""

		for len(cells) > 0 {
			end, columns := 0, 0

			for end < len(cells) && columns+runeWidth(cells[end].r) <= width {
				columns += runeWidth(cells[end].r)
				end++
			}

			// A word that does not fit goes to the next row whole, unless it
			// is longer than a row. A row that ends where a word does needs
			// no going back.
			if end < len(cells) && cells[end].r != ' ' {
				if space := lastSpace(cells[:end]); space > 0 {
					end = space + 1
				}
			}

			var row strings.Builder

			row.WriteString(styles)
			shown := end

			for shown > 0 && cells[shown-1].r == ' ' {
				shown--
			}

			for _, each := range cells[:end] {
				styles = carried(styles, each.lead)
			}

			for _, each := range cells[:shown] {
				row.WriteString(each.lead)
				row.WriteRune(each.r)
			}

			cells = cells[end:]

			// The next row starts where the next word does.
			for end > 0 && len(cells) > 0 && cells[0].r == ' ' {
				styles = carried(styles, cells[0].lead)
				cells = cells[1:]
			}

			if len(cells) == 0 {
				row.WriteString(trailing)
			}

			rows = append(rows, row.String())
		}

		if visibleWidth(line) == 0 {
			rows = append(rows, line)
		}
	}

	return rows
}

// cell is one rune that shows, with the escape sequences before it.
type cell struct {
	lead string
	r    rune
}

// cellsOf splits text into what shows, rune by rune, and the escape
// sequences after the last rune.
func cellsOf(text string) ([]cell, string) {
	var cells []cell
	var lead strings.Builder

	scan(text, func(r rune, escaped bool) bool {
		if escaped {
			lead.WriteRune(r)
		} else {
			cells = append(cells, cell{lead: lead.String(), r: r})
			lead.Reset()
		}

		return true
	})

	return cells, lead.String()
}

// carried is the styles in force after more escape sequences: what came
// since the last reset.
func carried(styles, more string) string {
	styles += more

	if index := strings.LastIndex(styles, styleReset); index >= 0 {
		styles = styles[index+len(styleReset):]
	}

	return styles
}

func lastSpace(cells []cell) int {
	for index := len(cells) - 1; index >= 0; index-- {
		if cells[index].r == ' ' {
			return index
		}
	}

	return -1
}

// runeWidth is how many columns a rune takes: none for one that combines
// with the one before, two for the wide ones of East Asian scripts and most
// emoji, and one for the rest.
func runeWidth(r rune) int {
	switch {
	case r == 0 || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || r == 0x200d || (r >= 0xfe00 && r <= 0xfe0f):
		return 0
	case r >= 0x1100 && r <= 0x115f,
		r >= 0x2e80 && r <= 0xa4cf && r != 0x303f,
		r >= 0xac00 && r <= 0xd7a3,
		r >= 0xf900 && r <= 0xfaff,
		r >= 0xfe30 && r <= 0xfe4f,
		r >= 0xff00 && r <= 0xff60,
		r >= 0xffe0 && r <= 0xffe6,
		r >= 0x1f300 && r <= 0x1f64f,
		r >= 0x1f900 && r <= 0x1f9ff,
		r >= 0x20000 && r <= 0x3fffd:
		return 2
	}

	return 1
}
