package tui

import (
	"slices"
	"strings"
	"unicode"
)

// inputLine is the line being typed: its text, where the cursor is in it,
// and which part of it the screen shows when it is wider than the screen.
// The keys that edit it are readline's.
type inputLine struct {
	text []rune
	// cursor is the position of the cursor, from 0 before the first rune to
	// len(text) after the last.
	cursor int
	// offset is the first rune shown. It moves only as far as the cursor
	// needs, so the text does not jump while the cursor moves.
	offset int
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
}

func (l *inputLine) clear() {
	l.set("")
}

func (l *inputLine) insert(r rune) {
	l.text = slices.Insert(l.text, l.cursor, r)
	l.cursor++
}

// backspace deletes the rune before the cursor.
func (l *inputLine) backspace() {
	if l.cursor > 0 {
		l.text = slices.Delete(l.text, l.cursor-1, l.cursor)
		l.cursor--
	}
}

// remove deletes the rune under the cursor.
func (l *inputLine) remove() {
	if l.cursor < len(l.text) {
		l.text = slices.Delete(l.text, l.cursor, l.cursor+1)
	}
}

func (l *inputLine) left() {
	l.cursor = max(l.cursor-1, 0)
}

func (l *inputLine) right() {
	l.cursor = min(l.cursor+1, len(l.text))
}

func (l *inputLine) home() {
	l.cursor = 0
}

func (l *inputLine) end() {
	l.cursor = len(l.text)
}

// wordLeft moves to the start of the word before the cursor.
func (l *inputLine) wordLeft() {
	l.cursor = l.wordStart()
}

// wordRight moves past the end of the word after the cursor.
func (l *inputLine) wordRight() {
	for l.cursor < len(l.text) && unicode.IsSpace(l.text[l.cursor]) {
		l.cursor++
	}

	for l.cursor < len(l.text) && !unicode.IsSpace(l.text[l.cursor]) {
		l.cursor++
	}
}

// deleteWord deletes the word before the cursor, with the spaces between
// it and the cursor.
func (l *inputLine) deleteWord() {
	start := l.wordStart()

	l.text = slices.Delete(l.text, start, l.cursor)
	l.cursor = start
}

// killStart deletes from the start of the line to the cursor.
func (l *inputLine) killStart() {
	l.text = slices.Delete(l.text, 0, l.cursor)
	l.cursor = 0
}

// killEnd deletes from the cursor to the end of the line.
func (l *inputLine) killEnd() {
	l.text = l.text[:l.cursor]
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

// view returns the part of the text that fits in width columns, with the
// cursor in it, and the cursor's column in that part. A masked line shows
// a dot for each rune.
func (l *inputLine) view(width int, masked bool) (string, int) {
	width = max(width, 1)

	// The cursor can sit after the last rune, which takes a column too.
	switch {
	case l.cursor < l.offset:
		l.offset = l.cursor
	case l.cursor >= l.offset+width:
		l.offset = l.cursor - width + 1
	}

	// Text deleted at the end gives its room back to what is before it.
	l.offset = max(min(l.offset, len(l.text)+1-width), 0)

	shown := l.text[l.offset:min(l.offset+width, len(l.text))]
	column := l.cursor - l.offset

	if masked {
		return strings.Repeat("•", len(shown)), column
	}

	return string(shown), column
}
