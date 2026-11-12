package transcript

import (
	"encoding/json"
	"testing"

	"rumbo/internal/store"
	"rumbo/protocol"
)

func entry(seq int64, kind string, payload any) store.Entry {
	raw, _ := json.Marshal(payload)

	return store.Entry{Seq: seq, Kind: kind, Payload: raw}
}

// A tool call that never got a result, because the run was cancelled, is
// closed with an error result so the history stays valid for the provider.
func TestBuildClosesInterruptedCalls(t *testing.T) {
	call := protocol.Block{Type: protocol.BlockToolCall, ID: "a", Name: "bash", Args: json.RawMessage(`{}`)}

	entries := []store.Entry{
		entry(1, KindUser, Text{Text: "go"}),
		entry(2, KindAssistant, Assistant{Message: protocol.Message{Role: protocol.RoleAssistant, Content: []protocol.Block{call}}}),
		entry(3, KindUser, Text{Text: "continue"}),
	}

	state, err := Build(entries)

	if err != nil {
		t.Fatal(err)
	}

	if len(state.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(state.Messages))
	}

	last := state.Messages[2].Content

	if len(last) != 2 || last[0].Type != protocol.BlockToolResult || last[0].ID != "a" || !last[0].IsError || last[1].Text != "continue" {
		t.Fatalf("unexpected final user message: %+v", last)
	}
}

func TestBuildTracksSections(t *testing.T) {
	entries := []store.Entry{
		entry(1, KindContext, Context{Sections: []Section{{Name: "tree", Text: "a.go"}, {Name: "git", Text: "clean"}}}),
		entry(2, KindContext, Context{Sections: []Section{{Name: "tree", Text: "a.go\nb.go"}, {Name: "git"}}}),
	}

	state, err := Build(entries)

	if err != nil {
		t.Fatal(err)
	}

	if len(state.Sections) != 1 || state.Sections["tree"] != "a.go\nb.go" {
		t.Fatalf("sections = %v", state.Sections)
	}
}

func TestBuildStartsOverAtACompaction(t *testing.T) {
	answer := Assistant{
		Message: protocol.Message{Role: protocol.RoleAssistant, Content: []protocol.Block{{Type: protocol.BlockText, Text: "old"}}},
		Usage:   protocol.Usage{Input: 10, CacheRead: 20, CacheWrite: 5, Output: 7},
	}

	entries := []store.Entry{
		entry(1, KindContext, Context{Sections: []Section{{Name: "tree", Text: "a.go"}, {Name: "git", Text: "clean"}}}),
		entry(2, KindUser, Text{Text: "go"}),
		entry(3, KindAssistant, answer),
		entry(4, KindContext, Context{Sections: []Section{{Name: "tree", Text: "a.go\nb.go"}}}),
	}

	before, err := Build(entries)

	if err != nil || before.Tokens != 42 {
		t.Fatalf("tokens = %d, err = %v, want the sum the provider reported", before.Tokens, err)
	}

	entries = append(entries,
		entry(5, KindCompaction, Compaction{Text: "summary"}),
		entry(6, KindUser, Text{Text: "next"}),
	)

	state, err := Build(entries)

	if err != nil {
		t.Fatal(err)
	}

	if len(state.Messages) != 1 || state.Tokens != 0 {
		t.Fatalf("messages = %d, tokens = %d, want one message and a reset count", len(state.Messages), state.Tokens)
	}

	content := state.Messages[0].Content
	want := "<context name=\"tree\">\na.go\nb.go\n</context>\n\n<context name=\"git\">\nclean\n</context>"

	if len(content) != 3 || content[0].Text != want || content[1].Text != "summary" || content[2].Text != "next" {
		t.Fatalf("content after compaction = %+v", content)
	}
}

// Opaque blocks are tied to the prefix that produced them, so a new system
// prompt in the middle of a session removes the ones already there.
func TestBuildDropsOpaqueBlocksWhenTheSystemPromptChanges(t *testing.T) {
	answer := func(text string) Assistant {
		return Assistant{Message: protocol.Message{Role: protocol.RoleAssistant, Content: []protocol.Block{
			{Type: protocol.BlockOpaque, Data: json.RawMessage(`{"signature":"s"}`)},
			{Type: protocol.BlockText, Text: text},
		}}}
	}

	entries := []store.Entry{
		entry(1, KindSystem, System{Text: "v1"}),
		entry(2, KindUser, Text{Text: "one"}),
		entry(3, KindAssistant, answer("first")),
		entry(4, KindSystem, System{Text: "v2"}),
		entry(5, KindUser, Text{Text: "two"}),
		entry(6, KindAssistant, answer("second")),
	}

	state, err := Build(entries)

	if err != nil {
		t.Fatal(err)
	}

	first, second := state.Messages[1].Content, state.Messages[3].Content

	if len(first) != 1 || first[0].Text != "first" {
		t.Fatalf("the block from before the change was kept: %+v", first)
	}

	if len(second) != 2 || second[0].Type != protocol.BlockOpaque {
		t.Fatalf("the block from after the change was dropped: %+v", second)
	}
}
