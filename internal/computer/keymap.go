package computer

import (
	"fmt"
	"strings"
)

// The virtual keyboard is a US keyboard: each key has the Linux input code
// of the key it is, so a program that reads codes, as browsers do for
// shortcuts, finds what it expects. A character such a keyboard does not
// have, such as ñ or €, is typed on a letter's key in a keymap given for it
// alone, and the US one comes back after. The protocol numbers keys by
// their Linux code; XKB numbers each one 8 higher.

// usKey is a key that types a character: its code, and what it types alone
// and with Shift, as X keysym names.
type usKey struct {
	code           uint32
	plain, shifted string
}

// usKeys are the keys of a US keyboard that type characters.
var usKeys = []usKey{
	{2, "1", "exclam"}, {3, "2", "at"}, {4, "3", "numbersign"}, {5, "4", "dollar"},
	{6, "5", "percent"}, {7, "6", "asciicircum"}, {8, "7", "ampersand"}, {9, "8", "asterisk"},
	{10, "9", "parenleft"}, {11, "0", "parenright"}, {12, "minus", "underscore"}, {13, "equal", "plus"},
	{16, "q", "Q"}, {17, "w", "W"}, {18, "e", "E"}, {19, "r", "R"}, {20, "t", "T"}, {21, "y", "Y"},
	{22, "u", "U"}, {23, "i", "I"}, {24, "o", "O"}, {25, "p", "P"}, {26, "bracketleft", "braceleft"},
	{27, "bracketright", "braceright"}, {30, "a", "A"}, {31, "s", "S"}, {32, "d", "D"}, {33, "f", "F"},
	{34, "g", "G"}, {35, "h", "H"}, {36, "j", "J"}, {37, "k", "K"}, {38, "l", "L"},
	{39, "semicolon", "colon"}, {40, "apostrophe", "quotedbl"}, {41, "grave", "asciitilde"},
	{43, "backslash", "bar"}, {44, "z", "Z"}, {45, "x", "X"}, {46, "c", "C"}, {47, "v", "V"},
	{48, "b", "B"}, {49, "n", "N"}, {50, "m", "M"}, {51, "comma", "less"}, {52, "period", "greater"},
	{53, "slash", "question"},
}

// namedCodes are the codes of the keys that type no character, by the
// keysym they have.
var namedCodes = map[string]uint32{
	"Escape": 1, "BackSpace": 14, "Tab": 15, "Return": 28, "space": 57, "Caps_Lock": 58,
	"F1": 59, "F2": 60, "F3": 61, "F4": 62, "F5": 63, "F6": 64, "F7": 65, "F8": 66, "F9": 67, "F10": 68,
	"Num_Lock": 69, "Scroll_Lock": 70, "F11": 87, "F12": 88, "KP_Enter": 96, "Print": 99,
	"Home": 102, "Up": 103, "Page_Up": 104, "Left": 105, "Right": 106, "End": 107, "Down": 108,
	"Page_Down": 109, "Insert": 110, "Delete": 111, "XF86AudioMute": 113, "XF86AudioLowerVolume": 114,
	"XF86AudioRaiseVolume": 115, "Pause": 119, "Menu": 127, "XF86AudioPlay": 164,
	"F13": 183, "F14": 184, "F15": 185, "F16": 186, "F17": 187, "F18": 188, "F19": 189, "F20": 190,
	"F21": 191, "F22": 192, "F23": 193, "F24": 194,
}

// Each modifier's key, its keysym, the XKB modifier it sets, and that
// modifier's bit in the masks Wayland clients read.
var modifierKeys = map[Modifier]struct {
	code   uint32
	keysym string
	xkb    string
	mask   uint32
}{
	Shift:   {42, "Shift_L", "Shift", 1},
	Control: {29, "Control_L", "Control", 4},
	Alt:     {56, "Alt_L", "Mod1", 8},
	Super:   {125, "Super_L", "Mod4", 64},
}

// stroke is how a keysym is typed on the US keyboard: its key, and whether
// Shift is held for it.
type stroke struct {
	code  uint32
	shift bool
}

// usStrokes finds each keysym of the US keyboard.
var usStrokes = func() map[string]stroke {
	found := map[string]stroke{}

	for name, code := range namedCodes {
		found[name] = stroke{code: code}
	}

	for _, key := range usKeys {
		found[key.plain] = stroke{code: key.code}
		found[key.shifted] = stroke{code: key.code, shift: true}
	}

	for _, modifier := range modifierOrder {
		found[modifierKeys[modifier].keysym] = stroke{code: modifierKeys[modifier].code}
	}

	return found
}()

// usStroke returns how a keysym is typed on the US keyboard, and whether it
// is there.
func usStroke(keysym string) (stroke, bool) {
	found, ok := usStrokes[keysym]

	return found, ok
}

// extraKeys is how many characters a keymap for others can hold: one on
// each key that types one.
var extraKeys = len(usKeys)

// keymap returns the US keyboard in XKB's text format. extra puts other
// characters, by keysym, on keys that type characters, in place of theirs.
func keymap(extra map[string]uint32) string {
	symbols := map[uint32]string{}

	for name, code := range namedCodes {
		symbols[code] = name
	}

	for _, key := range usKeys {
		symbols[key.code] = key.plain + ", " + key.shifted
	}

	for _, modifier := range modifierOrder {
		symbols[modifierKeys[modifier].code] = modifierKeys[modifier].keysym
	}

	for name, code := range extra {
		symbols[code] = name
	}

	var out strings.Builder

	out.WriteString("xkb_keymap {\nxkb_keycodes \"rumbo\" {\nminimum = 8;\nmaximum = 255;\n")

	// Keys in the order of their codes, so the same keymap is the same text.
	for code := uint32(1); code <= 247; code++ {
		if _, ok := symbols[code]; ok {
			fmt.Fprintf(&out, "<K%d> = %d;\n", code, code+8)
		}
	}

	out.WriteString("};\nxkb_types \"rumbo\" { include \"complete\" };\nxkb_compat \"rumbo\" { include \"complete\" };\nxkb_symbols \"rumbo\" {\n")

	for code := uint32(1); code <= 247; code++ {
		if names, ok := symbols[code]; ok {
			fmt.Fprintf(&out, "key <K%d> {[ %s ]};\n", code, names)
		}
	}

	for _, modifier := range modifierOrder {
		fmt.Fprintf(&out, "modifier_map %s { <K%d> };\n", modifierKeys[modifier].xkb, modifierKeys[modifier].code)
	}

	out.WriteString("};\n};\n")

	return out.String()
}

// placeExtra gives each keysym, in order, a key that types a character, and
// returns the keys given. There are extraKeys of them; the caller types no
// more different characters than that with one keymap.
func placeExtra(names []string) map[string]uint32 {
	placed := map[string]uint32{}

	for _, name := range names {
		if _, ok := placed[name]; ok {
			continue
		}

		placed[name] = usKeys[len(placed)].code
	}

	return placed
}
