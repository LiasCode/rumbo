package transcript

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/protocol"
)

// A session's history tells its runs in the events a front end saw while
// they worked, and adds what only the history holds: what the person asked
// and how each run ended.
func TestHistoryTellsRunsAsTheyWereSeen(t *testing.T) {
	ls := protocol.Block{Type: protocol.BlockToolCall, ID: "a", Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)}
	cat := protocol.Block{Type: protocol.BlockToolCall, ID: "b", Name: "read", Args: json.RawMessage(`{"path":"x"}`)}
	opaque := protocol.Block{Type: protocol.BlockOpaque, Data: json.RawMessage(`{}`)}
	usage := protocol.Usage{Input: 10, CacheRead: 20, Output: 5}

	answer := func(blocks ...protocol.Block) Assistant {
		return Assistant{Message: protocol.Message{Role: protocol.RoleAssistant, Content: blocks}, Usage: usage, Window: 1000}
	}

	text := func(text string) protocol.Block {
		return protocol.Block{Type: protocol.BlockText, Text: text}
	}

	entries := []store.Entry{
		entry(1, KindSystem, System{Text: "prompt"}),
		entry(2, KindContext, Context{Sections: []Section{{Name: "environment", Text: "linux"}}}),
		entry(3, KindUser, Text{Text: "list the files"}),
		entry(4, KindAssistant, answer(text("Looking."), ls, opaque)),
		entry(5, KindToolResult, ToolResult{ID: "a", Name: "bash", Text: "x"}),
		entry(6, KindSteer, Text{Text: "and read x"}),
		entry(7, KindNote, Note{Type: NoteRetry, Text: "request failed; retrying"}),
		entry(8, KindAssistant, answer(text("One file."))),
		entry(9, KindNote, Note{Type: NoteDone}),
		entry(10, KindUser, Text{Text: "read it"}),
		entry(11, KindAssistant, answer(cat)),
		entry(12, KindNote, Note{Type: NoteCancelled}),
		entry(13, KindHarness, Text{Text: "[harness] continue"}),
		entry(14, KindCompaction, Compaction{Text: "summary", Usage: protocol.Usage{Input: 3, Output: 2}}),
		entry(15, KindNote, Note{Type: NoteFailed, Text: "the provider is gone"}),
	}

	events, err := History(entries)

	if err != nil {
		t.Fatal(err)
	}

	turn := protocol.Event{Type: protocol.EventTurn, Usage: &usage, Context: 35, Window: 1000}
	compacted := protocol.Usage{Input: 3, Output: 2}

	want := []protocol.Event{
		{Type: protocol.EventContext, Sections: []string{"environment"}},
		{Type: protocol.EventObjective, Text: "list the files"},
		{Type: protocol.EventText, Text: "Looking."},
		turn,
		{Type: protocol.EventToolStarted, Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)},
		{Type: protocol.EventToolFinished, Name: "bash", Result: "x"},
		{Type: protocol.EventSteer, Text: "and read x"},
		{Type: protocol.EventLog, Text: "request failed; retrying", Source: protocol.TargetHost},
		{Type: protocol.EventText, Text: "One file."},
		turn,
		{Type: protocol.EventEnded, Status: NoteDone},
		{Type: protocol.EventObjective, Text: "read it"},
		turn,
		// The call the cancel left without a result is told as started.
		{Type: protocol.EventToolStarted, Name: "read", Args: json.RawMessage(`{"path":"x"}`)},
		{Type: protocol.EventEnded, Status: NoteCancelled},
		{Type: protocol.EventLog, Text: Compacted, Source: protocol.TargetHost},
		{Type: protocol.EventTurn, Usage: &compacted},
		{Type: protocol.EventEnded, Status: NoteFailed, Text: "the provider is gone"},
	}

	if !reflect.DeepEqual(events, want) {
		got, _ := json.MarshalIndent(events, "", "  ")
		t.Fatalf("history:\n%s", got)
	}
}

// A process that died in the middle of a tool call leaves the call without
// a result and the run without an end; the history still tells the call.
func TestHistoryTellsACallLeftOpenAtTheEnd(t *testing.T) {
	call := protocol.Block{Type: protocol.BlockToolCall, ID: "a", Name: "bash"}

	events, err := History([]store.Entry{
		entry(1, KindUser, Text{Text: "go"}),
		entry(2, KindAssistant, Assistant{Message: protocol.Message{Role: protocol.RoleAssistant, Content: []protocol.Block{call}}}),
	})

	if err != nil {
		t.Fatal(err)
	}

	if last := events[len(events)-1]; last.Type != protocol.EventToolStarted || last.Name != "bash" {
		t.Fatalf("last event = %+v, want the open call", last)
	}
}

func TestHistoryRefusesUnknownEntries(t *testing.T) {
	if _, err := History([]store.Entry{entry(1, "mystery", Text{})}); err == nil {
		t.Fatal("an unknown entry was told")
	}
}
