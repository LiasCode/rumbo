package computer

import (
	"strings"
	"testing"
)

func TestKeysAreReadAsXdotoolWritesThem(t *testing.T) {
	for text, want := range map[string]Keys{
		"Return":       {Key: "Return"},
		"enter":        {Key: "Return"},
		"ctrl+s":       {Modifiers: Control, Key: "s"},
		"Ctrl+Shift+t": {Modifiers: Control | Shift, Key: "t"},
		"ctrl+T":       {Modifiers: Control | Shift, Key: "t"},
		"super":        {Modifiers: Super},
		"cmd+c":        {Modifiers: Super, Key: "c"},
		"alt+F4":       {Modifiers: Alt, Key: "F4"},
		"ctrl++":       {Modifiers: Control, Key: "+"},
		"+":            {Key: "+"},
		"ctrl+plus":    {Modifiers: Control, Key: "+"},
		"Page_Down":    {Key: "Page_Down"},
		"pgup":         {Key: "Page_Up"},
		"ñ":            {Key: "ñ"},
		"A":            {Key: "A"},
	} {
		got, err := ParseKeys(text)

		if err != nil || got != want {
			t.Errorf("ParseKeys(%q) = %+v, %v; want %+v", text, got, err, want)
		}
	}
}

func TestKeysThatCannotBeReadSayWhy(t *testing.T) {
	for text, says := range map[string]string{
		"notakey":  "unknown key",
		"ctrl+":    "empty part",
		"a+ctrl":   "not a modifier",
		"ctrl+x+y": "not a modifier",
	} {
		if _, err := ParseKeys(text); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("ParseKeys(%q) error = %v, want it to say %q", text, err, says)
		}
	}

	if _, err := ParseSequence("  "); err == nil {
		t.Error("an empty sequence was accepted")
	}
}

func TestASequenceIsPressedInTurn(t *testing.T) {
	sequence, err := ParseSequence("ctrl+a BackSpace")

	if err != nil || len(sequence) != 2 || sequence[0] != (Keys{Modifiers: Control, Key: "a"}) || sequence[1] != (Keys{Key: "BackSpace"}) {
		t.Fatalf("sequence = %+v, %v", sequence, err)
	}
}

func TestEveryCharacterHasAKeysym(t *testing.T) {
	for key, want := range map[string]string{
		"a": "a", "Z": "Z", "7": "7", " ": "space", ",": "comma", "+": "plus",
		"\n": "Return", "\t": "Tab", "ñ": "U00F1", "€": "U20AC", "😀": "U1F600",
		"Return": "Return", "F5": "F5",
	} {
		if got := keysym(key); got != want {
			t.Errorf("keysym(%q) = %q, want %q", key, got, want)
		}
	}
}

// Each key has the code of the key it is on a US keyboard, so programs that
// read codes, as browsers do, are not told a letter is Tab or Enter.
func TestKeysHaveTheCodesOfAUSKeyboard(t *testing.T) {
	for keysym, want := range map[string]stroke{
		"a": {code: 30}, "A": {code: 30, shift: true}, "1": {code: 2}, "exclam": {code: 2, shift: true},
		"Return": {code: 28}, "Tab": {code: 15}, "BackSpace": {code: 14}, "Escape": {code: 1},
		"space": {code: 57}, "Control_L": {code: 29}, "Shift_L": {code: 42}, "Super_L": {code: 125},
		"plus": {code: 13, shift: true}, "Left": {code: 105},
	} {
		if got, ok := usStroke(keysym); !ok || got != want {
			t.Errorf("usStroke(%q) = %+v, %v; want %+v", keysym, got, ok, want)
		}
	}

	if _, ok := usStroke("U00F1"); ok {
		t.Error("ñ is not on a US keyboard")
	}

	codes := map[uint32]string{}

	for name, found := range usStrokes {
		if other, taken := codes[found.code]; taken && !found.shift && !usStrokes[other].shift {
			t.Errorf("%s and %s share key %d", name, other, found.code)
		}

		if !found.shift {
			codes[found.code] = name
		}
	}
}

// Characters a US keyboard lacks go on the keys of characters, never on a
// key such as Tab or Enter.
func TestOtherCharactersGoOnTheKeysOfCharacters(t *testing.T) {
	names := make([]string, 0, extraKeys)

	for character := rune(0x100); len(names) < extraKeys; character++ {
		names = append(names, keysym(string(character)))
	}

	placed := placeExtra(append(names, names[0]))

	if len(placed) != extraKeys {
		t.Fatalf("placed %d keys, want %d", len(placed), extraKeys)
	}

	characterKeys := map[uint32]bool{}

	for _, key := range usKeys {
		characterKeys[key.code] = true
	}

	used := map[uint32]bool{}

	for name, code := range placed {
		if !characterKeys[code] || used[code] {
			t.Fatalf("%s is on key %d", name, code)
		}

		used[code] = true
	}

	text := keymap(map[string]uint32{"U00F1": 30})

	if !strings.Contains(text, "key <K30> {[ U00F1 ]};") || !strings.Contains(text, "key <K31> {[ s, S ]};") {
		t.Fatalf("keymap with ñ on a:\n%s", text)
	}
}

func TestTheKeymapNamesItsModifiers(t *testing.T) {
	text := keymap(nil)

	for _, line := range []string{
		"<K30> = 38;", "key <K30> {[ a, A ]};", "key <K28> {[ Return ]};",
		"modifier_map Shift { <K42> };", "modifier_map Control { <K29> };",
		"modifier_map Mod1 { <K56> };", "modifier_map Mod4 { <K125> };",
	} {
		if !strings.Contains(text, line) {
			t.Errorf("the keymap has no %q", line)
		}
	}

	if keymap(nil) != text {
		t.Error("the same keymap is not the same text")
	}

	if mask(Control|Shift) != 5 || mask(Alt|Super) != 72 {
		t.Errorf("masks = %d, %d", mask(Control|Shift), mask(Alt|Super))
	}
}

func TestAPPMIsRead(t *testing.T) {
	img, err := decodePPM([]byte("P6\n# grim\n2 1\n255\n\xff\x00\x00\x00\x00\xff"))

	if err != nil {
		t.Fatal(err)
	}

	if img.Bounds().Dx() != 2 || img.Bounds().Dy() != 1 {
		t.Fatalf("size = %v", img.Bounds())
	}

	if red, blue := img.RGBAAt(0, 0), img.RGBAAt(1, 0); red.R != 255 || red.A != 255 || blue.B != 255 || blue.R != 0 {
		t.Fatalf("pixels = %v %v", red, blue)
	}

	for _, broken := range []string{"P5\n1 1\n255\n\x00", "P6\n2 2\n255\n\x00", "P6\n1 1\n65535\n\x00\x00"} {
		if _, err := decodePPM([]byte(broken)); err == nil {
			t.Errorf("decodePPM(%q) gave no error", broken)
		}
	}
}

func TestPlacesAreInsideTheirArea(t *testing.T) {
	area := Rect{X: 100, Y: 50, Width: 10, Height: 10}

	if !area.Contains(Point{X: 100, Y: 50}) || !area.Contains(Point{X: 109, Y: 59}) || area.Contains(Point{X: 110, Y: 55}) {
		t.Fatal("Contains is wrong at the edges")
	}
}
