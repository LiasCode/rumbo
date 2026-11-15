package tui

import "testing"

// typed is a line with text and the cursor at |.
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

// shown is the line with | at the cursor.
func shown(line *inputLine) string {
	return string(line.text[:line.cursor]) + "|" + string(line.text[line.cursor:])
}

func TestTheLineEditsAtTheCursor(t *testing.T) {
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
	} {
		line := typed(test.before)
		test.edit(line)

		if got := shown(line); got != test.after {
			t.Errorf("%q: got %q, want %q", test.before, got, test.after)
		}
	}
}

// A line wider than the screen shows the part around the cursor, and only
// scrolls as far as the cursor needs.
func TestALongLineScrollsWithTheCursor(t *testing.T) {
	line := typed("0123456789|")

	if text, column := line.view(4, false); text != "789" || column != 3 {
		t.Fatalf("at the end: %q, %d", text, column)
	}

	line.left()
	line.left()

	if text, column := line.view(4, false); text != "789" || column != 1 {
		t.Fatalf("two to the left: %q, %d", text, column)
	}

	line.home()

	if text, column := line.view(4, false); text != "0123" || column != 0 {
		t.Fatalf("at home: %q, %d", text, column)
	}

	line.set("ab")

	if text, column := line.view(4, true); text != "••" || column != 2 {
		t.Fatalf("masked after set: %q, %d", text, column)
	}
}
