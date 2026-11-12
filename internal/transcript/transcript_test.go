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
