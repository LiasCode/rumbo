package tui

import (
	"io"
	"strings"
	"testing"
)

// screen is a ui that draws nowhere, for what happens to the input line.
func screen() *ui {
	return &ui{out: io.Discard, width: 80, height: 24}
}

// Up goes back through what was entered, Down comes forward again, and past
// the newest line is what was being typed before going back.
func TestTheArrowsBringBackWhatWasEntered(t *testing.T) {
	u := screen()

	for _, line := range []string{"fix the test", "/status", "/status", "add a flag"} {
		u.remember(line)
	}

	u.input = []rune("half typ")

	// Ctrl-P and Ctrl-N, which the arrows stand for.
	var shown []string

	for _, key := range []rune{0x10, 0x10, 0x10, 0x10, 0x0e, 0x0e, 0x0e} {
		u.key(key)
		shown = append(shown, string(u.input))
	}

	// A line entered twice in a row is kept once, and going further back
	// than the oldest line, or forward from the draft, stays where it is.
	want := []string{"add a flag", "/status", "fix the test", "fix the test", "/status", "add a flag", "half typ"}

	if strings.Join(shown, " | ") != strings.Join(want, " | ") {
		t.Fatalf("shown %q, want %q", shown, want)
	}

	if u.key(0x0e); string(u.input) != "half typ" {
		t.Fatalf("past the draft: %q", string(u.input))
	}
}

// The answer to a confirmation is not kept, since bringing it back later
// would send it as a task.
func TestAConfirmationAnswerIsNotKept(t *testing.T) {
	u := screen()
	u.remember("/delete 4cf0261c")
	u.pending = func() (answer, error) { return answer{}, nil }
	u.input = []rune("no")

	u.key('\r')
	u.wait.Wait()

	if len(u.history) != 1 || u.history[0] != "/delete 4cf0261c" {
		t.Fatalf("history = %q", u.history)
	}

	if u.key(0x10); string(u.input) != "/delete 4cf0261c" {
		t.Fatalf("up after the answer: %q", string(u.input))
	}
}
