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
