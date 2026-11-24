package tui

import (
	"strings"
	"testing"
)

// typed is a text with the cursor at |.
func typed(text string) *inputLine {
	line := &inputLine{}

	for _, r := range text {
		if r == '|' {
			line.cursor = len(line.text)
			continue
		}

		line.text = append(line.text, r)
	}

	return line
}

// shown is the text with | at the cursor.
func shown(line *inputLine) string {
	return string(line.text[:line.cursor]) + "|" + string(line.text[line.cursor:])
}

func TestTheInputEditsAtTheCursor(t *testing.T) {
	for _, test := range []struct {
		before string
		edit   func(*inputLine)
		after  string
	}{
		{"fix th|", func(l *inputLine) { l.insert('e') }, "fix the|"},
		{"fx| it", func(l *inputLine) { l.left(); l.insert('i'); l.right() }, "fix| it"},
		{"|abc", func(l *inputLine) { l.left(); l.backspace() }, "|abc"},
		{"ab|c", func(l *inputLine) { l.backspace() }, "a|c"},
		{"ab|c", func(l *inputLine) { l.remove() }, "ab|"},
		{"abc|", func(l *inputLine) { l.remove(); l.right() }, "abc|"},
		{"add a fl|ag", func(l *inputLine) { l.home() }, "|add a flag"},
		{"|add a flag", func(l *inputLine) { l.end() }, "add a flag|"},
		{"add a  flag|", func(l *inputLine) { l.wordLeft() }, "add a  |flag"},
		{"add a  |flag", func(l *inputLine) { l.wordLeft() }, "add |a  flag"},
		{"|add a flag", func(l *inputLine) { l.wordRight() }, "add| a flag"},
		{"add| a flag", func(l *inputLine) { l.wordRight() }, "add a| flag"},
		{"add a flag  |", func(l *inputLine) { l.deleteWord() }, "add a |"},
		{"add a| flag", func(l *inputLine) { l.deleteWord() }, "add | flag"},
		{"add a| flag", func(l *inputLine) { l.killStart() }, "| flag"},
		{"add a| flag", func(l *inputLine) { l.killEnd() }, "add a|"},
		{"ñan|dú", func(l *inputLine) { l.backspace(); l.insert('d') }, "ñad|dú"},

		// With line breaks, Home and End and the kills keep to the line the
		// cursor is on, and at its edge join it to the next or the one before.
		{"first\nsec|ond\nthird", func(l *inputLine) { l.home() }, "first\n|second\nthird"},
		{"first\nsec|ond\nthird", func(l *inputLine) { l.end() }, "first\nsecond|\nthird"},
		{"first\nsec|ond\nthird", func(l *inputLine) { l.killStart() }, "first\n|ond\nthird"},
		{"first\n|second", func(l *inputLine) { l.killStart() }, "first|second"},
		{"first|\nsecond", func(l *inputLine) { l.killEnd() }, "first|second"},
		{"one|", func(l *inputLine) { l.insert('\n'); l.insert('t') }, "one\nt|"},
	} {
		line := typed(test.before)
		test.edit(line)

		if got := shown(line); got != test.after {
			t.Errorf("%q: got %q, want %q", test.before, got, test.after)
		}
	}
}

// The input takes as many rows as its text needs: one for each line, and
// more for a line wider than the screen. The cursor is on the row and the
// column it is at, also after a full row.
func TestTheInputTakesTheRowsItsTextNeeds(t *testing.T) {
	for _, test := range []struct {
		text   string
		rows   []string
		row    int
		column int
	}{
		{"fix|", []string{"fix"}, 0, 3},
		{"0123456789|", []string{"0123", "4567", "89"}, 2, 2},
		{"0123|", []string{"0123", ""}, 1, 0},
		{"one\ntw|o", []string{"one", "two"}, 1, 2},
		{"one\n|", []string{"one", ""}, 1, 0},
		{"日本語|", []string{"日本", "語"}, 1, 2},
	} {
		rows, row, column := typed(test.text).view(4, 10, false)

		if strings.Join(rows, "|") != strings.Join(test.rows, "|") || row != test.row || column != test.column {
			t.Errorf("%q: rows %q, cursor %d:%d; want %q, %d:%d", test.text, rows, row, column, test.rows, test.row, test.column)
		}
	}

	if rows, _, column := typed("ab|").view(4, 10, true); rows[0] != "••" || column != 2 {
		t.Fatalf("masked: %q, column %d", rows, column)
	}
}

// Up and Down move through the rows, keeping to the column they started
// from across a shorter row, and say when there is no row further.
func TestUpAndDownMoveThroughTheRows(t *testing.T) {
	line := typed("a long first line\nab\nthe third line|")
	line.view(40, 10, false)

	moves := []struct {
		move func() bool
		want string
		ok   bool
	}{
		{line.up, "a long first line\nab|\nthe third line", true},
		{line.up, "a long first l|ine\nab\nthe third line", true},
		{line.up, "a long first l|ine\nab\nthe third line", false},
		{line.down, "a long first line\nab|\nthe third line", true},
		{line.down, "a long first line\nab\nthe third line|", true},
		{line.down, "a long first line\nab\nthe third line|", false},
	}

	// The cursor starts at column 14, which the first line has too.
	line.home()
	line.end()

	for index, step := range moves {
		ok := step.move()

		if got := shown(line); got != step.want || ok != step.ok {
			t.Fatalf("move %d: %q, %v; want %q, %v", index, got, ok, step.want, step.ok)
		}
	}
}

// When the text takes more rows than the input shows, it shows the ones
// around the cursor, and only scrolls as far as the cursor needs.
func TestALongInputScrollsWithTheCursor(t *testing.T) {
	line := typed("1\n2\n3\n4\n5|")

	if rows, row, _ := line.view(10, 3, false); strings.Join(rows, "") != "345" || row != 2 {
		t.Fatalf("at the end: %q, row %d", rows, row)
	}

	line.up()

	if rows, row, _ := line.view(10, 3, false); strings.Join(rows, "") != "345" || row != 1 {
		t.Fatalf("one row up: %q, row %d", rows, row)
	}

	line.cursor = 0

	if rows, row, _ := line.view(10, 3, false); strings.Join(rows, "") != "123" || row != 0 {
		t.Fatalf("at the start: %q, row %d", rows, row)
	}
}
