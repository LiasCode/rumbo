package tui

import (
	"bytes"
	"context"
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

	u.input.set("half typ")

	// Ctrl-P and Ctrl-N, which the arrows stand for.
	var shown []string

	for _, key := range []rune{0x10, 0x10, 0x10, 0x10, 0x0e, 0x0e, 0x0e} {
		u.key(key)
		shown = append(shown, u.input.String())
	}

	// A line entered twice in a row is kept once, and going further back
	// than the oldest line, or forward from the draft, stays where it is.
	want := []string{"add a flag", "/status", "fix the test", "fix the test", "/status", "add a flag", "half typ"}

	if strings.Join(shown, " | ") != strings.Join(want, " | ") {
		t.Fatalf("shown %q, want %q", shown, want)
	}

	if u.key(0x0e); u.input.String() != "half typ" {
		t.Fatalf("past the draft: %q", u.input.String())
	}
}

// The answer to a confirmation is not kept, since bringing it back later
// would send it as a task.
func TestAConfirmationAnswerIsNotKept(t *testing.T) {
	u := screen()
	u.remember("/delete 4cf0261c")
	u.pending = func() (answer, error) { return answer{}, nil }
	u.input.set("no")

	u.key('\r')
	u.wait.Wait()

	if len(u.history) != 1 || u.history[0] != "/delete 4cf0261c" {
		t.Fatalf("history = %q", u.history)
	}

	if u.key(0x10); u.input.String() != "/delete 4cf0261c" {
		t.Fatalf("up after the answer: %q", u.input.String())
	}
}

// A function's secret question takes the next line typed: the function gets
// it, the screen never shows it, and the history does not keep it.
func TestASecretAnswerIsNeitherShownNorKept(t *testing.T) {
	var drawn bytes.Buffer

	u := screen()
	u.out = &drawn
	answered := make(chan string, 1)

	go func() {
		answer, _ := talk{ui: u}.Ask(context.Background(), "the code the page shows", true)
		answered <- answer
	}()

	for asked := false; !asked; {
		u.mu.Lock()
		asked = u.question != nil
		u.mu.Unlock()
	}

	for _, key := range "s3cret" {
		u.key(key)
	}

	u.key('\r')

	if answer := <-answered; answer != "s3cret" {
		t.Fatalf("the function got %q", answer)
	}

	if strings.Contains(drawn.String(), "s3cret") || !strings.Contains(drawn.String(), "••••••") {
		t.Fatal("the screen showed the secret, or did not show that something was typed")
	}

	if len(u.history) != 0 {
		t.Fatalf("history = %q", u.history)
	}
}

// The keys edit where the cursor is, as readline's do.
func TestTheKeysEditWhereTheCursorIs(t *testing.T) {
	u := screen()

	for _, key := range "fix test" {
		u.key(key)
	}

	// Ctrl-B back to the start of "test", then type: "fix the test".
	for range 4 {
		u.key(0x02)
	}

	for _, key := range "the " {
		u.key(key)
	}

	if got := shown(&u.input); got != "fix the |test" {
		t.Fatalf("after inserting: %q", got)
	}

	u.key(0x01) // Ctrl-A
	u.key(0x04) // Ctrl-D deletes under the cursor on a line with text.
	u.key(0x05) // Ctrl-E
	u.key(0x17) // Ctrl-W

	if got := shown(&u.input); got != "ix the |" {
		t.Fatalf("after deleting: %q", got)
	}

	u.edit((*inputLine).wordLeft)
	u.key(0x0b) // Ctrl-K

	if got := shown(&u.input); got != "ix |" {
		t.Fatalf("after killing to the end: %q", got)
	}
}
