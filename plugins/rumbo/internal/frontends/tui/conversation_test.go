package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LiasCode/rumbo/protocol"
)

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

// kinds lists the kinds of what a conversation shows, in order.
func kinds(c *conversation) []int {
	listed := make([]int, len(c.entries))

	for index, each := range c.entries {
		listed[index] = each.kind
	}

	return listed
}

// plainRows is what a conversation shows at width, without its styles.
func plainRows(c *conversation, width int) string {
	rows := c.rows(defaultTheme, width, time.Now())

	for index, row := range rows {
		rows[index] = strings.TrimRight(stripped(row), " ")
	}

	return strings.Join(rows, "\n")
}

// The model's text streams into one reply until something else comes, and
// a turn's usage is no such thing.
func TestTextStreamsIntoOneReply(t *testing.T) {
	c := newConversation("s1", func(string) string { return "" })
	now := time.Now()

	for _, event := range []protocol.Event{
		{Type: protocol.EventText, Text: "Hi"},
		{Type: protocol.EventTurn, Usage: &protocol.Usage{Input: 10}},
		{Type: protocol.EventText, Text: " there"},
		{Type: protocol.EventContext, Sections: []string{"environment"}},
		{Type: protocol.EventText, Text: "Again"},
	} {
		c.apply(event, false, now)
	}

	if got := kinds(c); fmt.Sprint(got) != fmt.Sprint([]int{entryReply, entryContext, entryReply}) {
		t.Fatalf("kinds %v", got)
	}

	if c.entries[0].text != "Hi there" || c.entries[0].open || !c.entries[2].open || c.reply != "Again" {
		t.Fatalf("replies %q (open %v), %q (open %v); last answer %q", c.entries[0].text, c.entries[0].open, c.entries[2].text, c.entries[2].open, c.reply)
	}
}

// A result goes to the oldest call of its tool still working, and a new
// task leaves the calls that never finished as they stand.
func TestAResultGoesToTheOldestCallOfItsTool(t *testing.T) {
	c := newConversation("s1", func(string) string { return "" })
	now := time.Now()

	for _, event := range []protocol.Event{
		{Type: protocol.EventToolStarted, Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)},
		{Type: protocol.EventToolStarted, Name: "read", Args: json.RawMessage(`{"path":"a.txt"}`)},
		{Type: protocol.EventToolStarted, Name: "bash", Args: json.RawMessage(`{"command":"false"}`)},
		{Type: protocol.EventToolFinished, Name: "bash", Result: "a.txt"},
	} {
		c.apply(event, false, now)
	}

	if !c.entries[0].finished || c.entries[0].result != "a.txt" || !c.entries[1].working() || !c.entries[2].working() || !c.working() {
		t.Fatalf("calls: %+v %+v %+v", c.entries[0], c.entries[1], c.entries[2])
	}

	c.ask("go on", nil, false, now)

	if c.working() || c.entries[1].why != "it never finished" {
		t.Fatalf("after a new task: working %v, why %q", c.working(), c.entries[1].why)
	}

	if shown := plainRows(c, 60); !inOrder(shown, "✓ bash ls", "    a.txt", "· read a.txt  (it never finished)", "· bash false  (it never finished)", "go on") {
		t.Fatalf("shown:\n%s", shown)
	}
}

// A session's history is told before what the conversation shows: each
// objective before the context its run started with, a call that never
// finished said to be so, how each run ended, and what they spent.
func TestAHistoryIsToldBeforeWhatFollows(t *testing.T) {
	c := newConversation("s1", func(source string) string { return "[" + source + "]" })
	c.add(&entry{kind: entryAnswer, text: "an answer"})

	c.told([]protocol.Event{
		{Type: protocol.EventContext, Sections: []string{"environment"}},
		{Type: protocol.EventObjective, Text: "fix it"},
		{Type: protocol.EventText, Text: "On it."},
		{Type: protocol.EventToolStarted, Name: "edit", Args: json.RawMessage(`{"path":"a.go","old":"x","new":"y"}`)},
		{Type: protocol.EventTurn, Usage: &protocol.Usage{Input: 1200, Output: 40}, Context: 1300, Window: 200000},
		{Type: protocol.EventEnded, Status: protocol.RunCancelled, Took: 83000},
	}, time.Now())

	want := []int{entryTask, entryContext, entryReply, entryTool, entryEnded, entryAnswer}

	if got := kinds(c); fmt.Sprint(got) != fmt.Sprint(want) || !c.loaded {
		t.Fatalf("kinds %v, loaded %v", got, c.loaded)
	}

	if context, window := c.usage.Fill(); context != 1300 || window != 200000 || c.usage.Total().Input != 1200 {
		t.Fatalf("usage: context %d of %d, %+v", context, window, c.usage.Total())
	}

	if shown := plainRows(c, 60); !inOrder(shown, "fix it", "context: environment", "On it.", "· edit a.go  (it never finished)", "✗ cancelled  1m23s · 1.2k in · 40 out · 0 cached (0%)", "an answer") {
		t.Fatalf("shown:\n%s", shown)
	}
}

// What the person said shows in a box on the right of the column, what the
// model wrote as Markdown, and no row is wider than the column.
func TestTheConversationIsDrawnInItsColumn(t *testing.T) {
	c := newConversation("s1", func(string) string { return "" })
	now := time.Now()

	c.ask("fix\tthe failing test", nil, false, now)
	c.apply(protocol.Event{Type: protocol.EventText, Text: "## Plan\nRun **the tests**, which take a long while to finish, and then fix what fails.\n```go\n\tfmt.Println(1)\n```\n"}, false, now)
	c.end(protocol.RunDone, "", now)

	rows := c.rows(defaultTheme, 40, now)

	for _, row := range rows {
		if visibleWidth(row) > 40 {
			t.Fatalf("a row is wider than the column: %q", stripped(row))
		}
	}

	shown := plainRows(c, 40)
	lines := strings.Split(shown, "\n")

	if !strings.HasSuffix(lines[0], "╮") || len([]rune(lines[0])) != 40 || !strings.Contains(lines[1], "│ fix    the failing test │") {
		t.Fatalf("the person's words are not on the right:\n%s", shown)
	}

	if !inOrder(shown, "Plan", "Run the tests, which take a long while\nto finish, and then fix what fails.", "    fmt.Println(1)", "✓ done") {
		t.Fatalf("shown:\n%s", shown)
	}
}
