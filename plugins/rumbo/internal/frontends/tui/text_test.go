package tui

import (
	"strings"
	"testing"
)

func TestTheWidthOfTextIsWhatItShows(t *testing.T) {
	for text, want := range map[string]int{
		"plain":                         5,
		styleBold + "bold" + styleReset: 4,
		"日本":                            4,
		"é":                            1,
		escape + "7saved":               5,
	} {
		if got := visibleWidth(text); got != want {
			t.Errorf("visibleWidth(%q) = %d, want %d", text, got, want)
		}
	}

	if got := cut(styleDim+"abcdefgh"+styleReset, 5); visibleWidth(got) != 5 || !strings.HasPrefix(got, styleDim+"abcd") || !strings.Contains(got, "…") {
		t.Fatalf("cut = %q", got)
	}

	if got := cut("short", 10); got != "short" {
		t.Fatalf("cut of what fits = %q", got)
	}

	if got := wrap("the quick brown fox", 9); strings.Join(got, "|") != "the quick|brown fox" {
		t.Fatalf("wrap = %q", got)
	}

	if got := wrap("abcdefghij", 4); strings.Join(got, "|") != "abcd|efgh|ij" {
		t.Fatalf("wrap of a long word = %q", got)
	}

	// Styles take no room, and one that goes on past a row starts the next.
	if got := wrap("plain "+styleBold+"bold words"+styleReset+" end", 10); strings.Join(got, "|") != "plain "+styleBold+"bold|"+styleBold+"words"+styleReset+" end" {
		t.Fatalf("wrap of styled text = %q", got)
	}

	if got := wrap("one\n\ntwo", 10); strings.Join(got, "|") != "one||two" {
		t.Fatalf("wrap of an empty line = %q", got)
	}
}
