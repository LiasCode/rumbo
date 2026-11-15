package tui

import (
	"bufio"
	"errors"
	"strings"
)

// key is one key press: a rune typed or a control character, or a key with
// a name, which the terminal sends as an escape sequence.
type key struct {
	r    rune
	name string
}

// Keys with a name.
const (
	keyUp         = "up"
	keyDown       = "down"
	keyLeft       = "left"
	keyRight      = "right"
	keyHome       = "home"
	keyEnd        = "end"
	keyDelete     = "delete"
	keyPageUp     = "page-up"
	keyPageDown   = "page-down"
	keyWordLeft   = "word-left"
	keyWordRight  = "word-right"
	keyBackTab    = "back-tab"
	keyNewline    = "newline"
	keyEscape     = "escape"
	keyPasteStart = "paste-start"
	keyPasteEnd   = "paste-end"
)

// sequences are what terminals send for the keys with a name, after the
// escape. Several send different ones for the same key.
var sequences = map[string]string{
	"[A": keyUp, "OA": keyUp,
	"[B": keyDown, "OB": keyDown,
	"[C": keyRight, "OC": keyRight,
	"[D": keyLeft, "OD": keyLeft,
	"[H": keyHome, "OH": keyHome, "[1~": keyHome, "[7~": keyHome,
	"[F": keyEnd, "OF": keyEnd, "[4~": keyEnd, "[8~": keyEnd,
	"[3~": keyDelete,
	"[5~": keyPageUp,
	"[6~": keyPageDown,
	// With Ctrl or Alt, Left and Right move by words.
	"[1;5D": keyWordLeft, "[1;3D": keyWordLeft,
	"[1;5C": keyWordRight, "[1;3C": keyWordRight,
	"[Z": keyBackTab,
	// Shift-Enter and Alt-Enter, from terminals that tell them apart from
	// Enter: kitty's keyboard protocol, and xterm's other keys.
	"[13;2u": keyNewline, "[13;3u": keyNewline, "[27;2;13~": keyNewline, "[27;3;13~": keyNewline,
	"[200~": keyPasteStart,
	"[201~": keyPasteEnd,
}

// readKey reads one key press. A lone escape is the Escape key: a terminal
// writes a sequence at once, so the rest of one is already read when its
// escape is.
func readKey(keys *bufio.Reader) (key, error) {
	r, _, err := keys.ReadRune()

	if err != nil || r != 0x1b {
		return key{r: r}, err
	}

	if keys.Buffered() == 0 {
		return key{name: keyEscape}, nil
	}

	next, _, err := keys.ReadRune()

	if err != nil {
		return key{}, err
	}

	switch next {
	// Alt-B and Alt-F move by words, as in readline, and Alt-Enter breaks
	// the line.
	case 'b':
		return key{name: keyWordLeft}, nil
	case 'f':
		return key{name: keyWordRight}, nil
	case '\r', '\n':
		return key{name: keyNewline}, nil
	case '[', 'O':
	default:
		// Alt with another key does nothing.
		return key{}, nil
	}

	var sequence strings.Builder
	sequence.WriteRune(next)

	for {
		r, _, err := keys.ReadRune()

		if err != nil {
			return key{}, err
		}

		sequence.WriteRune(r)

		// A sequence ends with a letter or a tilde; after O, with any
		// character.
		if next == 'O' || (r >= 0x40 && r <= 0x7e && sequence.Len() > 1) {
			return key{name: sequences[sequence.String()]}, nil
		}

		if sequence.Len() > 32 {
			return key{}, errors.New("escape sequence too long")
		}
	}
}
