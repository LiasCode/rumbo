package tui

import (
	"bytes"
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
}

// What is done goes above the live area once, and the live area is drawn
// again from where it starts, wherever the cursor was left in it.
func TestTheLiveAreaIsDrawnAgainInPlace(t *testing.T) {
	var out bytes.Buffer

	s := screen{out: &out, width: 40, height: 20}
	s.paint("", []string{"menu", "> typed", "status"}, 1, 7)

	if !strings.HasSuffix(out.String(), "\r"+escape+"[7C"+syncEnd) || !strings.Contains(out.String(), escape+"[1A") {
		t.Fatalf("first draw: %q", out.String())
	}

	out.Reset()
	s.paint("done\n", []string{"> ", "status"}, 0, 2)

	// Up the one row the cursor was on, clear, then what is done, then the
	// live area.
	if want := syncStart + "\r" + escape + "[1A" + clearBelow + "done\r\n> "; !strings.HasPrefix(out.String(), want) {
		t.Fatalf("second draw: %q, want it to start with %q", out.String(), want)
	}

	// Taller than the screen, the live area keeps its last rows.
	out.Reset()
	s.height = 3
	s.paint("", []string{"a", "b", "c", "d"}, 3, 0)

	if strings.Contains(out.String(), "a\r\n") || len(s.drawn) != 2 || s.row != 1 {
		t.Fatalf("cut to the screen: %q, rows %q, row %d", out.String(), s.drawn, s.row)
	}
}
