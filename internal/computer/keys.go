package computer

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Modifier is a key held to change what others mean.
type Modifier int

// The modifiers, as flags.
const (
	Shift Modifier = 1 << iota
	Control
	Alt
	// Super is the key with the system's logo, Command on a Mac.
	Super
)

// modifierOrder is the order modifiers go down in.
var modifierOrder = []Modifier{Control, Alt, Shift, Super}

// Keys is a combination: the modifiers held, and the key pressed with
// them. Key is an X keysym name, such as Return or F5, or one character;
// it is empty when only modifiers are pressed.
type Keys struct {
	Modifiers Modifier
	Key       string
}

// modifierNames are the names a combination may give a modifier.
var modifierNames = aliases(map[Modifier][]string{
	Shift:   {"shift"},
	Control: {"ctrl", "control", "ctl"},
	Alt:     {"alt", "option", "opt"},
	Super:   {"super", "win", "windows", "logo", "meta", "cmd", "command"},
})

// keyNames are the keys known by name, each with the names a combination
// may give it, by the X keysym name it maps to.
var keyNames = aliases(map[string][]string{
	"Return":               {"return", "enter"},
	"KP_Enter":             {"kp_enter"},
	"Tab":                  {"tab"},
	"Escape":               {"escape", "esc"},
	"BackSpace":            {"backspace"},
	"Delete":               {"delete", "del"},
	"Insert":               {"insert", "ins"},
	"Home":                 {"home"},
	"End":                  {"end"},
	"Page_Up":              {"page_up", "pageup", "pgup", "prior"},
	"Page_Down":            {"page_down", "pagedown", "pgdn", "next"},
	"Left":                 {"left", "arrowleft"},
	"Right":                {"right", "arrowright"},
	"Up":                   {"up", "arrowup"},
	"Down":                 {"down", "arrowdown"},
	"space":                {"space"},
	"Menu":                 {"menu"},
	"Print":                {"print", "printscreen"},
	"Pause":                {"pause"},
	"Caps_Lock":            {"caps_lock", "capslock"},
	"Num_Lock":             {"num_lock"},
	"Scroll_Lock":          {"scroll_lock"},
	"XF86AudioRaiseVolume": {"xf86audioraisevolume"},
	"XF86AudioLowerVolume": {"xf86audiolowervolume"},
	"XF86AudioMute":        {"xf86audiomute"},
	"XF86AudioPlay":        {"xf86audioplay"},
})

// aliases turns each value's names into a map from each name to it.
func aliases[T comparable](named map[T][]string) map[string]T {
	out := map[string]T{}

	for value, names := range named {
		for _, name := range names {
			out[name] = value
		}
	}

	return out
}

// characterNames are the characters X names, which a combination may give
// by name as well, such as ctrl+plus.
var characterNames = map[rune]string{
	' ': "space", '!': "exclam", '"': "quotedbl", '#': "numbersign", '$': "dollar",
	'%': "percent", '&': "ampersand", '\'': "apostrophe", '(': "parenleft", ')': "parenright",
	'*': "asterisk", '+': "plus", ',': "comma", '-': "minus", '.': "period", '/': "slash",
	':': "colon", ';': "semicolon", '<': "less", '=': "equal", '>': "greater", '?': "question",
	'@': "at", '[': "bracketleft", '\\': "backslash", ']': "bracketright", '^': "asciicircum",
	'_': "underscore", '`': "grave", '{': "braceleft", '|': "bar", '}': "braceright", '~': "asciitilde",
}

func init() {
	for number := 1; number <= 24; number++ {
		name := fmt.Sprintf("F%d", number)
		keyNames[strings.ToLower(name)] = name
	}
}

// byCharacterName finds a character by its X name.
var byCharacterName = func() map[string]rune {
	found := map[string]rune{}

	for character, name := range characterNames {
		found[strings.ToLower(name)] = character
	}

	return found
}()

// ParseSequence reads combinations separated by spaces, as xdotool takes
// them: "ctrl+a Delete" presses one, then the other.
func ParseSequence(text string) ([]Keys, error) {
	fields := strings.Fields(text)

	if len(fields) == 0 {
		return nil, fmt.Errorf("no keys given: name them, such as Return, ctrl+s, or super")
	}

	sequence := make([]Keys, len(fields))

	for index, field := range fields {
		keys, err := ParseKeys(field)

		if err != nil {
			return nil, err
		}

		sequence[index] = keys
	}

	return sequence, nil
}

// ParseKeys reads one combination, its names joined by +, such as
// ctrl+shift+t, Return, or alt. Names are not case sensitive, except a
// single character's.
func ParseKeys(text string) (Keys, error) {
	var keys Keys

	// A trailing + is the key itself, as in ctrl++.
	parts := strings.Split(text, "+")

	switch {
	case text == "+":
		parts = []string{"+"}
	case strings.HasSuffix(text, "++"):
		parts = append(strings.Split(strings.TrimSuffix(text, "++"), "+"), "+")
	}

	for index, part := range parts {
		if part == "" {
			return Keys{}, fmt.Errorf("%q has an empty part; join keys with +, such as ctrl+s", text)
		}

		if modifier, ok := modifierNames[strings.ToLower(part)]; ok {
			keys.Modifiers |= modifier

			continue
		}

		if index != len(parts)-1 {
			return Keys{}, fmt.Errorf("%q: %q is not a modifier; only the last key of a combination can be another key", text, part)
		}

		key, err := keyName(part)

		if err != nil {
			return Keys{}, err
		}

		keys.Key = key
	}

	// A letter pressed with modifiers is the key that carries it, and a
	// capital one needs Shift, as on a keyboard.
	if keys.Modifiers != 0 && utf8.RuneCountInString(keys.Key) == 1 {
		letter, _ := utf8.DecodeRuneInString(keys.Key)

		if unicode.IsUpper(letter) {
			keys.Modifiers |= Shift
		}

		keys.Key = string(unicode.ToLower(letter))
	}

	return keys, nil
}

// keyName returns the X keysym name of a key named in a combination, or the
// character it is.
func keyName(name string) (string, error) {
	if utf8.RuneCountInString(name) == 1 {
		return name, nil
	}

	lower := strings.ToLower(name)

	if key, ok := keyNames[lower]; ok {
		return key, nil
	}

	if character, ok := byCharacterName[lower]; ok {
		return string(character), nil
	}

	return "", fmt.Errorf("unknown key %q: use a single character, or a name such as Return, Escape, Tab, BackSpace, Delete, Home, End, Page_Up, Page_Down, Up, Down, Left, Right, space, or F1 to F24", name)
}

// keysym returns the X keysym name of a key as Keys holds it, which is how
// XKB keymaps and xdotool name it. A character without a name of its own is
// named by its code point, as XKB writes U20AC for €.
func keysym(key string) string {
	if utf8.RuneCountInString(key) != 1 {
		return key
	}

	character, _ := utf8.DecodeRuneInString(key)

	switch {
	case character == '\n' || character == '\r':
		return "Return"
	case character == '\t':
		return "Tab"
	case character < 0x80 && (unicode.IsLetter(character) || unicode.IsDigit(character)):
		return key
	}

	if name, ok := characterNames[character]; ok {
		return name
	}

	return fmt.Sprintf("U%04X", character)
}
