package tui

import (
	"strings"
	"unicode"
)

// Text is drawn with escape sequences for its styles, which take no room,
// and is measured, cut, and wrapped by the columns each rune takes.

// Terminal control sequences.
const (
	escape       = "\x1b"
	styleReset   = escape + "[0m"
	styleBold    = escape + "[1m"
	styleDim     = escape + "[2m"
	styleRed     = escape + "[31m"
	styleCyan    = escape + "[36m"
	styleMagenta = escape + "[35m"
	styleYellow  = escape + "[33m"
	styleReverse = escape + "[7m"
)

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
