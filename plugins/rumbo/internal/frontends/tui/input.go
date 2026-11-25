package tui

import (
	"slices"
	"strings"
	"unicode"
)

// inputLine is what is being typed: its text, which may hold line breaks,
// and where the cursor is in it. The keys that edit it are readline's, and
// a line break is one more character.
type inputLine struct {
	text []rune
	// cursor is the position of the cursor, from 0 before the first rune to
	// len(text) after the last.
	cursor int
	// goal is the column Up and Down keep to, so going through a shorter
	// row and back does not lose it, while aiming is true.
	goal   int
	aiming bool
	// offset is the first row shown when the text takes more rows than the
	// input shows. It moves only as far as the cursor needs.
	offset int
	// width is the width the text was last laid out at, which Up and Down
	// move through.
	width int
}

func (l *inputLine) String() string {
	return string(l.text)
}

func (l *inputLine) empty() bool {
	return len(l.text) == 0
}

// set replaces the text, with the cursor after it, where typing goes on.
func (l *inputLine) set(text string) {
	l.text = []rune(text)
	l.cursor = len(l.text)
	l.aiming = false
}

func (l *inputLine) clear() {
	l.set("")
}

func (l *inputLine) insert(r rune) {
	l.text = slices.Insert(l.text, l.cursor, r)
	l.cursor++
	l.aiming = false
}

// backspace deletes the rune before the cursor.
func (l *inputLine) backspace() {
	if l.cursor > 0 {
		l.text = slices.Delete(l.text, l.cursor-1, l.cursor)
		l.cursor--
	}

	l.aiming = false
}

// remove deletes the rune under the cursor.
func (l *inputLine) remove() {
	if l.cursor < len(l.text) {
		l.text = slices.Delete(l.text, l.cursor, l.cursor+1)
	}

	l.aiming = false
}

func (l *inputLine) left() {
	l.cursor = max(l.cursor-1, 0)
	l.aiming = false
}

func (l *inputLine) right() {
	l.cursor = min(l.cursor+1, len(l.text))
	l.aiming = false
}

// home moves to the start of the line the cursor is on.
func (l *inputLine) home() {
	l.cursor = l.lineStart()
	l.aiming = false
}

// end moves to the end of the line the cursor is on.
func (l *inputLine) end() {
	l.cursor = l.lineEnd()
	l.aiming = false
}

// wordLeft moves to the start of the word before the cursor.
func (l *inputLine) wordLeft() {
	l.cursor = l.wordStart()
	l.aiming = false
}

// wordRight moves past the end of the word after the cursor.
func (l *inputLine) wordRight() {
	for l.cursor < len(l.text) && unicode.IsSpace(l.text[l.cursor]) {
		l.cursor++
	}

	for l.cursor < len(l.text) && !unicode.IsSpace(l.text[l.cursor]) {
		l.cursor++
	}

	l.aiming = false
}

// deleteWord deletes the word before the cursor, with the spaces between
// it and the cursor.
func (l *inputLine) deleteWord() {
	start := l.wordStart()

	l.text = slices.Delete(l.text, start, l.cursor)
	l.cursor = start
	l.aiming = false
}

// killStart deletes from the start of the line to the cursor. At the start
// of a line, it joins it to the one before.
func (l *inputLine) killStart() {
	start := l.lineStart()

	if start == l.cursor && start > 0 {
		start--
	}

	l.text = slices.Delete(l.text, start, l.cursor)
	l.cursor = start
	l.aiming = false
}

// killEnd deletes from the cursor to the end of the line. At the end of a
// line, it joins the next one to it.
func (l *inputLine) killEnd() {
	end := l.lineEnd()

	if end == l.cursor && end < len(l.text) {
		end++
	}

	l.text = slices.Delete(l.text, l.cursor, end)
	l.aiming = false
}

func (l *inputLine) lineStart() int {
	start := l.cursor

	for start > 0 && l.text[start-1] != '\n' {
		start--
	}

	return start
}

func (l *inputLine) lineEnd() int {
	end := l.cursor

	for end < len(l.text) && l.text[end] != '\n' {
		end++
	}

	return end
}

func (l *inputLine) wordStart() int {
	start := l.cursor

	for start > 0 && unicode.IsSpace(l.text[start-1]) {
		start--
	}

	for start > 0 && !unicode.IsSpace(l.text[start-1]) {
		start--
	}

	return start
}

// place is where a position of the text is shown: its row, and its column
// in the row.
type place struct {
	row    int
	column int
}

// places lays the text out in rows of width columns, a line break starting
// a row of its own, and returns where each position is shown, from before
// the first rune to after the last.
func (l *inputLine) places(width int) []place {
	width = max(width, 1)
	shown := make([]place, len(l.text)+1)
	row, column := 0, 0

	for index, r := range l.text {
		// A rune that does not fit, or the cursor after a full row, goes to
		// the next row.
		if column+runeWidth(r) > width {
			row, column = row+1, 0
		}

		shown[index] = place{row, column}

		if r == '\n' {
			row, column = row+1, 0
			continue
		}

		column += runeWidth(r)
	}

	if column >= width {
		row, column = row+1, 0
	}

	shown[len(l.text)] = place{row, column}

	return shown
}

// up moves the cursor to the row above, keeping its column as near as the
// row allows, and reports whether there was one.
func (l *inputLine) up() bool {
	return l.vertical(-1)
}

// down moves the cursor to the row below, and reports whether there was one.
func (l *inputLine) down() bool {
	return l.vertical(1)
}

func (l *inputLine) vertical(step int) bool {
	// Before it is shown, only line breaks start rows.
	width := l.width

	if width <= 0 {
		width = len(l.text) + 1
	}

	shown := l.places(width)
	from := shown[l.cursor]
	target := from.row + step

	if target < 0 || target > shown[len(shown)-1].row {
		return false
	}

	if !l.aiming {
		l.goal, l.aiming = from.column, true
	}

	// The last position of the target row at or before the goal column.
	best := -1

	for index, at := range shown {
		if at.row == target && (best < 0 || at.column <= l.goal) {
			best = index
		}
	}

	l.cursor = best

	return true
}

// atEnd reports whether the cursor is after the last rune.
func (l *inputLine) atEnd() bool {
	return l.cursor == len(l.text)
}

// view lays the text out in rows of width columns and returns the rows the
// input shows, at most height of them, with the row and column of the
// cursor among them. A masked text shows a dot for each rune.
func (l *inputLine) view(width, height int, masked bool) ([]string, int, int) {
	width, height = max(width, 1), max(height, 1)
	l.width = width

	shown := l.places(width)
	rows := make([]strings.Builder, shown[len(shown)-1].row+1)

	for index, r := range l.text {
		if r == '\n' {
			continue
		}

		if masked {
			r = '•'
		}

		rows[shown[index].row].WriteRune(r)
	}

	cursor := shown[l.cursor]

	switch {
	case cursor.row < l.offset:
		l.offset = cursor.row
	case cursor.row >= l.offset+height:
		l.offset = cursor.row - height + 1
	}

	// Text deleted at the end gives its room back to what is before it.
	l.offset = max(min(l.offset, len(rows)-height), 0)

	visible := make([]string, 0, height)

	for _, row := range rows[l.offset:min(l.offset+height, len(rows))] {
		visible = append(visible, row.String())
	}

	return visible, cursor.row - l.offset, cursor.column
}
