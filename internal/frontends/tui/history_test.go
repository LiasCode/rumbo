package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/LiasCode/rumbo/protocol"
)

// quiet is a ui that draws nowhere, for what happens to the input.
func quiet() *ui {
	return &ui{
		screen:    screen{out: &bytes.Buffer{}, width: 80, height: 24},
		history:   &history{},
		described: &protocol.DescribeResult{},
	}
}

// typing presses each rune of text as a key.
func typing(u *ui, text string) {
	for _, r := range text {
		u.press(key{r: r})
	}
}

// Up goes back through what was entered, Down comes forward again, and past
// the newest line is what was being typed before going back.
func TestTheArrowsBringBackWhatWasEntered(t *testing.T) {
	u := quiet()

	for _, line := range []string{"fix the test", "/status", "/status", "add a flag"} {
		u.history.add(line)
	}

	u.input.set("half typ")

	var shown []string

	for _, name := range []string{keyUp, keyUp, keyUp, keyUp, keyDown, keyDown, keyDown} {
		u.press(key{name: name})
		shown = append(shown, u.input.String())
	}

	// A line entered twice in a row is kept once, and going further back
	// than the oldest line, or forward from the draft, stays where it is.
	want := []string{"add a flag", "/status", "fix the test", "fix the test", "/status", "add a flag", "half typ"}

	if strings.Join(shown, " | ") != strings.Join(want, " | ") {
		t.Fatalf("shown %q, want %q", shown, want)
	}

	if u.press(key{r: 0x0e}); u.input.String() != "half typ" {
		t.Fatalf("past the draft: %q", u.input.String())
	}
}

// What the host recalls comes before what was entered here, the newest
// first, a line of several rows as one entry, which Up goes through before
// going further back.
func TestWhatWasAskedBeforeComesFirst(t *testing.T) {
	u := quiet()

	u.history.earlier([]string{"explain this\nand that", "fix the parser"})
	u.history.add("/status")

	var shown []string

	for range 5 {
		u.press(key{name: keyUp})
		shown = append(shown, u.input.String())
	}

	want := []string{"/status", "explain this\nand that", "explain this\nand that", "fix the parser", "fix the parser"}

	if strings.Join(shown, " | ") != strings.Join(want, " | ") {
		t.Fatalf("shown %q, want %q", shown, want)
	}
}

// Ctrl-R looks back for what is typed, further back with Ctrl-R again;
// Enter takes the entry into the input without sending it, and Escape
// leaves the input as it was.
func TestCtrlRSearchesWhatWasEntered(t *testing.T) {
	u := quiet()

	for _, line := range []string{"fix the parser", "write the docs", "fix the flags"} {
		u.history.add(line)
	}

	u.input.set("draft")
	u.press(key{r: 0x12})
	typing(u, "fix")

	if u.input.String() != "fix the flags" {
		t.Fatalf("first found: %q", u.input.String())
	}

	u.press(key{r: 0x12})

	if u.input.String() != "fix the parser" {
		t.Fatalf("found further back: %q", u.input.String())
	}

	u.press(key{r: '\r'})

	if u.searching || u.input.String() != "fix the parser" || u.here != nil {
		t.Fatalf("after Enter: searching %v, input %q", u.searching, u.input.String())
	}

	u.input.set("draft")
	u.press(key{r: 0x12})
	typing(u, "docs")
	u.press(key{name: keyEscape})

	if u.searching || u.input.String() != "draft" {
		t.Fatalf("after Escape: searching %v, input %q", u.searching, u.input.String())
	}
}

// The answer to a confirmation is not kept, since bringing it back later
// would send it as a task.
func TestAConfirmationAnswerIsNotKept(t *testing.T) {
	u := quiet()
	u.history.add("/delete 4cf0261c")
	u.pending = func() (answer, error) { return answer{}, nil }
	u.input.set("no")

	u.press(key{r: '\r'})
	u.wait.Wait()

	if len(u.history.entries) != 1 || u.history.entries[0] != "/delete 4cf0261c" {
		t.Fatalf("history = %q", u.history.entries)
	}

	if u.press(key{name: keyUp}); u.input.String() != "/delete 4cf0261c" {
		t.Fatalf("up after the answer: %q", u.input.String())
	}
}

// A function's secret question takes the next line typed: the function gets
// it, the screen never shows it, and the history does not keep it.
func TestASecretAnswerIsNeitherShownNorKept(t *testing.T) {
	var drawn bytes.Buffer

	u := quiet()
	u.screen.out = &drawn
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

	typing(u, "s3cret")
	u.press(key{r: '\r'})

	if answer := <-answered; answer != "s3cret" {
		t.Fatalf("the function got %q", answer)
	}

	if strings.Contains(drawn.String(), "s3cret") || !strings.Contains(drawn.String(), "••••••") {
		t.Fatal("the screen showed the secret, or did not show that something was typed")
	}

	if len(u.history.entries) != 0 {
		t.Fatalf("history = %q", u.history.entries)
	}
}

// The keys edit where the cursor is, as readline's do, and Alt-Enter or
// Ctrl-J breaks the line instead of sending it.
func TestTheKeysEditWhereTheCursorIs(t *testing.T) {
	u := quiet()
	typing(u, "fix test")

	// Ctrl-B back to the start of "test", then type: "fix the test".
	for range 4 {
		u.press(key{r: 0x02})
	}

	typing(u, "the ")

	if got := shown(&u.input); got != "fix the |test" {
		t.Fatalf("after inserting: %q", got)
	}

	u.press(key{r: 0x01}) // Ctrl-A
	u.press(key{r: 0x04}) // Ctrl-D deletes under the cursor on a line with text.
	u.press(key{r: 0x05}) // Ctrl-E
	u.press(key{r: 0x17}) // Ctrl-W

	if got := shown(&u.input); got != "ix the |" {
		t.Fatalf("after deleting: %q", got)
	}

	u.press(key{name: keyWordLeft})
	u.press(key{r: 0x0b}) // Ctrl-K

	if got := shown(&u.input); got != "ix |" {
		t.Fatalf("after killing to the end: %q", got)
	}

	u.press(key{name: keyNewline})
	u.press(key{r: '\n'})
	typing(u, "more")

	if got := shown(&u.input); got != "ix \n\nmore|" || u.here != nil {
		t.Fatalf("after breaking the line: %q", got)
	}
}
