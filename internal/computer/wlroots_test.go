//go:build linux

package computer

import (
	"strings"
	"testing"
)

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
