package tui

import (
	"strings"
	"testing"
)

// plain is what a styled text shows, without its escape sequences.
func plain(text string) string {
	return stripped(text)
}

func TestSpansInALineAreStyled(t *testing.T) {
	for _, test := range []struct {
		text  string
		want  string
		shown string
	}{
		{"run `go test`", "run " + defaultTheme.code + "go test" + styleColorOff, "run go test"},
		{"a **bold** word", "a " + styleBold + "bold" + styleWeightOff + " word", "a bold word"},
		{"an *italic* and _another_", "an " + styleItalic + "italic" + styleItalicOff + " and " + styleItalic + "another" + styleItalicOff, "an italic and another"},
		{"**`code` in bold**", styleBold + defaultTheme.code + "code" + styleColorOff + " in bold" + styleWeightOff, "code in bold"},
		{"see [the docs](https://x.dev)", "see " + styleUnderline + "the docs" + styleUnderlineOff + styleDim + " (https://x.dev)" + styleWeightOff, "see the docs (https://x.dev)"},

		// Marks nothing closes, or that are part of a word or of code, stay.
		{"snake_case_name", "snake_case_name", "snake_case_name"},
		{"2 * 3 * 4", "2 * 3 * 4", "2 * 3 * 4"},
		{"half **open", "half **open", "half **open"},
		{"`a *b*`", defaultTheme.code + "a *b*" + styleColorOff, "a *b*"},
		{`\*not italic\*`, "*not italic*", "*not italic*"},
	} {
		if got := defaultTheme.inline(test.text); got != test.want || plain(got) != test.shown {
			t.Errorf("inline(%q) = %q, want %q", test.text, got, test.want)
		}
	}
}

func TestBlocksAreShownAsWhatTheyAre(t *testing.T) {
	m := markdown{theme: defaultTheme}

	for _, test := range []struct {
		text  string
		shown string
		style string
	}{
		{"# Title", "Title", styleBold + styleUnderline},
		{"## Part ##", "Part", styleBold},
		{"- an item", "• an item", ""},
		{"  * nested", "  • nested", ""},
		{"1. first", "1. first", ""},
		{"> quoted", "│ quoted", styleDim},
		{"---", strings.Repeat("─", 39), styleDim},
		{"#hashtag", "#hashtag", ""},
	} {
		got := m.line(test.text, 40)

		if len(got) != 1 || plain(got[0]) != test.shown || !strings.HasPrefix(got[0], test.style) {
			t.Errorf("line(%q) = %q, want it to show %q", test.text, got, test.shown)
		}
	}
}

// Inside a code block nothing is styled but the code itself, and the block
// ends at a fence like the one that opened it.
func TestACodeBlockKeepsItsText(t *testing.T) {
	m := markdown{theme: defaultTheme}

	lines := []string{"```go", "x := a * b * c", "# not a heading", "~~~", "```", "**after**"}

	var shown []string

	for _, text := range lines {
		shown = append(shown, m.line(text, 40)...)
	}

	want := []string{"```go", "x := a * b * c", "# not a heading", "~~~", "```", "after"}

	for index, row := range shown {
		if plain(row) != want[index] {
			t.Fatalf("row %d = %q, want %q", index, plain(row), want[index])
		}
	}

	if !strings.HasPrefix(shown[1], defaultTheme.code) || !strings.HasPrefix(shown[5], styleBold) {
		t.Fatalf("styles: %q", shown)
	}

	// A longer fence holds a shorter one, which does not close it.
	nested := markdown{theme: defaultTheme}
	var rows []string

	for _, text := range []string{"````", "```", "**inside**", "```", "````", "**outside**"} {
		rows = append(rows, nested.line(text, 40)...)
	}

	if plain(rows[2]) != "**inside**" || plain(rows[5]) != "outside" {
		t.Fatalf("nested fences: %q", rows)
	}

	// A fence still being written opens nothing.
	m.peek("```", 40)

	if m.fence != "" {
		t.Fatal("peek opened a code block")
	}
}

// A table is held back until it ends, and then shown with its columns
// lined up.
func TestATableIsLinedUp(t *testing.T) {
	m := markdown{theme: defaultTheme}

	for _, text := range []string{"| name | size |", "|---|--:|", "| a | 1 |", "| **longer** | 22 |"} {
		if shown := m.line(text, 40); shown != nil {
			t.Fatalf("a row of the table was shown at once: %q", shown)
		}
	}

	if held := m.held(); len(held) != 4 {
		t.Fatalf("held = %q", held)
	}

	shown := m.line("after", 40)
	var rows []string

	for _, row := range shown {
		rows = append(rows, plain(row))
	}

	want := []string{"name   │ size", "───────┼─────", "a      │ 1   ", "longer │ 22  ", "after"}

	if strings.Join(rows, "\n") != strings.Join(want, "\n") {
		t.Fatalf("table:\n%s\nwant:\n%s", strings.Join(rows, "\n"), strings.Join(want, "\n"))
	}

	// One that ends with the text comes out at its end.
	m.line("| x | y |", 40)

	if ended := m.end(); len(ended) != 1 || plain(ended[0]) != "x │ y" {
		t.Fatalf("end = %q", ended)
	}
}
