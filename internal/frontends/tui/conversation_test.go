package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LiasCode/rumbo/protocol"
)

// drawn is what u's terminal shows after all it wrote: the scrollback and
// the live area under it, as lines, read by playing back the sequences the
// screen sends. Styles are left out.
func drawn(u *ui) string {
	var lines [][]rune

	row, column := 0, 0
	out := []rune(u.screen.out.(*bytes.Buffer).String())

	for index := 0; index < len(out); index++ {
		for len(lines) <= row {
			lines = append(lines, nil)
		}

		switch r := out[index]; {
		case r == 0x1b && index+1 < len(out) && out[index+1] == '[':
			end := index + 2

			for end < len(out) && (out[end] < 0x40 || out[end] > 0x7e) {
				end++
			}

			count := 1
			fmt.Sscanf(string(out[index+2:end]), "%d", &count)

			switch out[end] {
			case 'A':
				row = max(row-count, 0)
			case 'B':
				row++
			case 'C':
				column += count
			case 'K':
				lines[row] = nil
			}

			index = end

		case r == 0x1b:
			index++

		case r == '\r':
			column = 0

		case r == '\n':
			row++

		default:
			for len(lines[row]) < column {
				lines[row] = append(lines[row], ' ')
			}

			if column < len(lines[row]) {
				lines[row][column] = r
			} else {
				lines[row] = append(lines[row], r)
			}

			column++
		}
	}

	shown := make([]string, len(lines))

	for index, line := range lines {
		shown[index] = strings.TrimRight(string(line), " ")
	}

	return strings.Join(shown, "\n")
}

// inOrder reports whether text holds each part, one after the other.
func inOrder(text string, parts ...string) bool {
	for _, part := range parts {
		index := strings.Index(text, part)

		if index < 0 {
			return false
		}

		text = text[index+len(part):]
	}

	return true
}

// A run shows what the model writes as Markdown, each call as a block set
// apart from the text, and how it ended with what it took.
func TestARunIsShownAsItWorks(t *testing.T) {
	u := quiet()
	run := &job{tally: tally{started: time.Now()}}
	u.here = run

	for _, event := range []protocol.Event{
		{Type: protocol.EventText, Text: "## Pl"},
		{Type: protocol.EventText, Text: "an\nLooking.\n"},
		{Type: protocol.EventToolStarted, Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)},
		{Type: protocol.EventToolFinished, Name: "bash", Result: "a.txt"},
		{Type: protocol.EventTurn, Usage: &protocol.Usage{Input: 1500, Output: 20}},
		{Type: protocol.EventText, Text: "Done."},
		{Type: protocol.EventTurn, Usage: &protocol.Usage{Input: 1600, Output: 5}},
	} {
		u.tell(run, event)
	}

	u.end(run, protocol.RunResult{Status: protocol.RunDone}, nil)

	if shown := drawn(u); !inOrder(shown, "Plan\nLooking.\n\n✓ bash ls\n    a.txt\n\nDone.\n\n✓ done  ", "2 turns · 3.1k in, 0 cached, 25 out") {
		t.Fatalf("shown:\n%s", shown)
	}

	if u.tool != nil || u.partial != "" {
		t.Fatalf("left behind: tool %v, partial %q", u.tool, u.partial)
	}
}

// A session's history is told as its runs were shown: the objective before
// the context it started with, a call that never finished said to be so,
// and what the runs spent counted.
func TestAHistoryIsToldAsARunIs(t *testing.T) {
	u := quiet()

	u.retell(protocol.HistoryResult{Session: "4cf0261c", Events: []protocol.Event{
		{Type: protocol.EventContext, Sections: []string{"environment"}},
		{Type: protocol.EventObjective, Text: "fix it"},
		{Type: protocol.EventText, Text: "On it."},
		{Type: protocol.EventToolStarted, Name: "edit", Args: json.RawMessage(`{"path":"a.go","old":"x","new":"y"}`)},
		{Type: protocol.EventToolFinished, Name: "edit", Result: "edited a.go (1 replacement(s))"},
		{Type: protocol.EventTurn, Usage: &protocol.Usage{Input: 100, Output: 7}, Context: 107},
		{Type: protocol.EventEnded, Status: protocol.RunDone},
		{Type: protocol.EventObjective, Text: "and the tests"},
		{Type: protocol.EventToolStarted, Name: "bash", Args: json.RawMessage(`{"command":"go test"}`)},
	}})

	if shown := drawn(u); !inOrder(shown, "── session 4cf0261c", "> fix it", "context: environment", "On it.", "✓ edit a.go", "- x", "+ y", "✓ done  1 turn", "> and the tests", "· bash go test  (it never finished)") {
		t.Fatalf("shown:\n%s", shown)
	}

	if strings.Contains(drawn(u), "✓ done  0.") {
		t.Fatal("a run told by a history has a time")
	}

	if got := u.usage.String(); got != "100 in, 0 cached, 7 out" || u.usage.Context() == "" {
		t.Fatalf("usage = %q, context %q", got, u.usage.Context())
	}
}

// The status bar says what is happening, the model, and the session on the
// left, and how full the context is and what was spent on the right, and
// leaves out what does not fit, the least needed first.
func TestTheStatusBarKeepsWhatMattersMost(t *testing.T) {
	u := quiet()
	u.info.Provider, u.info.Model, u.session = "anthropic", "claude-opus-5-5", "4cf0261c"
	u.usage.Turn(protocol.Event{Usage: &protocol.Usage{Input: 6000, CacheRead: 4500, Output: 425}, Context: 84000, Window: 200000})

	now := time.Now()

	for width, want := range map[int][]string{
		120: {"ready · anthropic/claude-opus-5-5 · session 4cf0261c", "▰▰▰▰▱▱▱▱▱▱ 42% of 200k · 6k in, 4.5k cached, 425 out"},
		80:  {"ready · anthropic/claude-opus-5-5 · session 4cf0261c", "▰▰▰▰▱▱▱▱▱▱ 42% of 200k"},
		60:  {"ready · anthropic/claude-opus-5-5", "42% of 200k"},
		30:  {"ready"},
	} {
		got := plain(u.status(width, now))

		if visibleWidth(got) > width || !inOrder(got, want...) {
			t.Errorf("at %d columns: %q, want %q", width, got, want)
		}

		if width == 30 && strings.Contains(got, "session") {
			t.Errorf("at 30 columns: %q", got)
		}
	}

	if meter := plain(contextMeter(defaultTheme, 190000, 200000)); meter != "▰▰▰▰▰▰▰▰▰▱ 95% of 200k" || !strings.Contains(contextMeter(defaultTheme, 190000, 200000), styleRed) {
		t.Fatalf("a full context: %q", meter)
	}

	if meter := contextMeter(defaultTheme, 2200, 0); meter != "context 2.2k" {
		t.Fatalf("a context of no known size: %q", meter)
	}
}

// A run that ends while the terminal is not in front rings its bell and
// sends a notification; one that ends in front of the person does not.
func TestARunThatEndsAwayIsNotified(t *testing.T) {
	u := quiet()
	written := u.screen.out.(*bytes.Buffer)

	for _, unfocused := range []bool{false, true} {
		run := &job{label: "fix the parser\nand its tests", tally: tally{started: time.Now()}}
		u.here = run
		u.press(key{name: map[bool]string{false: keyFocusIn, true: keyFocusOut}[unfocused]})

		written.Reset()
		u.end(run, protocol.RunResult{Status: protocol.RunDone}, nil)

		notified := strings.Contains(written.String(), "\a"+escape+"]9;rumbo: done · fix the parser\a")

		if notified != unfocused {
			t.Fatalf("unfocused %v: notified %v, wrote %q", unfocused, notified, written.String())
		}
	}
}

// A run in the background counts what it takes, and says how it ended and
// where to see it.
func TestARunInTheBackgroundSaysHowItEnded(t *testing.T) {
	u := quiet()
	away := &job{session: "57ffcc97", tally: tally{started: time.Now()}}
	u.away = []*job{away}

	u.show(away, protocol.Event{Type: protocol.EventText, Run: 2, Text: "not shown"})
	u.show(away, protocol.Event{Type: protocol.EventTurn, Run: 2, Usage: &protocol.Usage{Input: 1200, Output: 30}})
	u.end(away, protocol.RunResult{Status: protocol.RunDone, Session: "57ffcc97"}, nil)

	shown := drawn(u)

	if strings.Contains(shown, "not shown") || !inOrder(shown, "◆ session 57ffcc97, in the background: ✓ done", "1 turn · 1.2k in, 0 cached, 30 out", "/open 57ffcc97 shows it") {
		t.Fatalf("shown:\n%s", shown)
	}

	if len(u.away) != 0 {
		t.Fatalf("still away: %v", u.away)
	}
}

// A run that goes to the background as the session changes leaves what it
// was showing in the scrollback, its call said to go on there, and nothing
// of it in the live area.
func TestARunLeftBehindTakesNothingOfTheLiveArea(t *testing.T) {
	u := quiet()
	run := &job{session: "4cf0261c", tally: tally{started: time.Now()}}
	u.here, u.session = run, "4cf0261c"

	u.tell(run, protocol.Event{Type: protocol.EventText, Text: "Looking at it"})
	u.tell(run, protocol.Event{Type: protocol.EventToolStarted, Name: "bash", Args: json.RawMessage(`{"command":"sleep 2"}`)})

	u.session = ""
	u.follow()
	u.footer()

	if u.tool != nil || u.here != nil || len(u.away) != 1 {
		t.Fatalf("tool %v, here %v, away %v", u.tool, u.here, u.away)
	}

	if shown := drawn(u); !inOrder(shown, "Looking at it", "· bash sleep 2  (it goes on in the background)") || strings.Count(shown, "sleep 2") != 1 {
		t.Fatalf("shown:\n%s", shown)
	}
}
