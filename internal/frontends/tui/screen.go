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
	// drawn are the rows of the live area as they were last drawn, and row
	// the one of them the cursor was left on, which is where drawing starts
	// again from.
	drawn []string
	row   int
}

// Terminal control sequences the screen uses.
const (
	clearBelow  = escape + "[J"
	syncStart   = escape + "[?2026h"
	syncEnd     = escape + "[?2026l"
	cursorUp    = escape + "[%dA"
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
	out.WriteString(s.top())
	out.WriteString(clearBelow)
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

	s.drawn, s.row = rows, row
}

// top returns the sequence that takes the cursor back to where the live
// area starts.
func (s *screen) top() string {
	if s.row == 0 {
		return "\r"
	}

	return "\r" + fmt.Sprintf(cursorUp, s.row)
}

// clear takes the live area away, leaving the cursor where it started, so
// the terminal goes on below the conversation.
func (s *screen) clear() {
	fmt.Fprint(s.out, s.top()+clearBelow)
	s.drawn, s.row = nil, 0
}

// fresh moves what the terminal shows into its scrollback, as clearing the
// screen of a shell does, and starts drawing again at the top.
func (s *screen) fresh() {
	fmt.Fprint(s.out, s.top()+clearBelow+strings.Repeat("\r\n", max(s.height-1, 0))+escape+"[H")
	s.drawn, s.row = nil, 0
}

// resized takes the new size. A terminal that wraps its lines again when it
// narrows makes each row drawn above the cursor take more than one, so they
// are counted at the new width, to find where the live area starts.
func (s *screen) resized(width, height int) {
	if width < s.width {
		rows := 0

		for _, line := range s.drawn[:min(s.row+1, len(s.drawn))] {
			rows += max((visibleWidth(line)+width-1)/width, 1)
		}

		s.row = max(rows-1, 0)
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
// can. It knows nothing of escape sequences, so it is for plain text.
func wrap(text string, width int) []string {
	width = max(width, 1)

	var rows []string

	for _, line := range strings.Split(text, "\n") {
		runes := []rune(line)

		for len(runes) > 0 {
			end, columns := 0, 0

			for end < len(runes) && columns+runeWidth(runes[end]) <= width {
				columns += runeWidth(runes[end])
				end++
			}

			// A word that does not fit goes to the next row whole, unless it
			// is longer than a row. A row that ends where a word does needs
			// no going back.
			if end < len(runes) && runes[end] != ' ' {
				if space := lastSpace(runes[:end]); space > 0 {
					end = space + 1
				}
			}

			rows = append(rows, strings.TrimRight(string(runes[:end]), " "))
			runes = runes[end:]

			// The next row starts where the next word does.
			for end > 0 && len(runes) > 0 && runes[0] == ' ' {
				runes = runes[1:]
			}
		}

		if len(line) == 0 {
			rows = append(rows, "")
		}
	}

	return rows
}

func lastSpace(runes []rune) int {
	for index := len(runes) - 1; index >= 0; index-- {
		if runes[index] == ' ' {
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
