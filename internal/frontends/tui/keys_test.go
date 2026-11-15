package tui

import (
	"bufio"
	"strings"
	"testing"
)

// What a terminal sends becomes keys: runes and control characters as they
// are, and escape sequences as the keys they stand for, in the several
// forms terminals send them.
func TestEscapeSequencesBecomeKeys(t *testing.T) {
	for sent, want := range map[string]key{
		"a":             {r: 'a'},
		"ñ":             {r: 'ñ'},
		"\r":            {r: '\r'},
		"\x03":          {r: 0x03},
		"\x1b[A":        {name: keyUp},
		"\x1bOB":        {name: keyDown},
		"\x1b[1;5D":     {name: keyWordLeft},
		"\x1bf":         {name: keyWordRight},
		"\x1b[3~":       {name: keyDelete},
		"\x1b[Z":        {name: keyBackTab},
		"\x1b\r":        {name: keyNewline},
		"\x1b[13;2u":    {name: keyNewline},
		"\x1b[27;2;13~": {name: keyNewline},
		"\x1b[200~":     {name: keyPasteStart},
		"\x1b[201~":     {name: keyPasteEnd},
		"\x1b":          {name: keyEscape},
		"\x1b[99X":      {},
		"\x1bx":         {},
	} {
		got, err := readKey(bufio.NewReader(strings.NewReader(sent)))

		if err != nil || got != want {
			t.Errorf("%q: %+v, %v; want %+v", sent, got, err, want)
		}
	}
}

// Keys sent one after another are read one at a time.
func TestKeysAreReadInTurn(t *testing.T) {
	keys := bufio.NewReader(strings.NewReader("a\x1b[Bb"))
	var got []key

	for range 3 {
		pressed, _ := readKey(keys)
		got = append(got, pressed)
	}

	if got[0] != (key{r: 'a'}) || got[1] != (key{name: keyDown}) || got[2] != (key{r: 'b'}) {
		t.Fatalf("keys = %+v", got)
	}
}
