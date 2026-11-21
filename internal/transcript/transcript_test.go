package transcript

import (
	"encoding/json"
	"testing"

	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/protocol"
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

// A session is branched before one of the person's objectives; what was
// said to a run while it worked is no such point.
func TestPointsAreTheObjectives(t *testing.T) {
	points, err := Points([]store.Entry{
		entry(1, KindSystem, System{Text: "prompt"}),
		entry(2, KindUser, Text{Text: "fix the parser"}),
		entry(3, KindSteer, Text{Text: "and the tests"}),
		entry(4, KindNote, Note{Type: NoteDone}),
		entry(5, KindUser, Text{Text: "now document it"}),
	})

	if err != nil || len(points) != 2 || points[0] != (Point{Seq: 2, Text: "fix the parser"}) || points[1] != (Point{Seq: 5, Text: "now document it"}) {
		t.Fatalf("points = %+v, err = %v", points, err)
	}
}

// What a tool's result showed the model is stored with it, and shown again
// on every later request.
func TestBuildKeepsTheImagesOfAResult(t *testing.T) {
	call := protocol.Block{Type: protocol.BlockToolCall, ID: "a", Name: "read", Args: json.RawMessage(`{"path":"shot.png"}`)}
	image := protocol.Image{MediaType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}}

	entries := []store.Entry{
		entry(1, KindUser, Text{Text: "look"}),
		entry(2, KindAssistant, Assistant{Message: protocol.Message{Role: protocol.RoleAssistant, Content: []protocol.Block{call}}}),
		entry(3, KindToolResult, ToolResult{ID: "a", Name: "read", Text: "[image shot.png: png, 1×1]", Images: []protocol.Image{image}}),
	}

	state, err := Build(entries)

	if err != nil {
		t.Fatal(err)
	}

	result := state.Messages[2].Content[0]

	if len(result.Images) != 1 || result.Images[0].MediaType != "image/png" || string(result.Images[0].Data) != string(image.Data) {
		t.Fatalf("result = %+v", result)
	}
}

// What a person gives with their words goes to the model with them, and the
// objective's images come back with the summary that replaces the
// conversation, which has only words for them.
func TestBuildShowsWhatAPersonGave(t *testing.T) {
	mockup := protocol.Image{MediaType: "image/png", Data: []byte("mockup")}
	crop := protocol.Image{MediaType: "image/png", Data: []byte("crop")}

	entries := []store.Entry{
		entry(1, KindUser, Text{Text: "build this", Images: []protocol.Image{mockup}}),
		entry(2, KindAssistant, Assistant{Message: protocol.Message{Role: protocol.RoleAssistant, Content: []protocol.Block{{Type: protocol.BlockText, Text: "ok"}}}}),
		entry(3, KindSteer, Text{Images: []protocol.Image{crop}}),
	}

	state, err := Build(entries)

	if err != nil {
		t.Fatal(err)
	}

	if first := state.Messages[0].Content[0]; first.Text != "build this" || len(first.Images) != 1 || string(first.Images[0].Data) != "mockup" {
		t.Fatalf("objective = %+v", first)
	}

	if steer := state.Messages[2].Content[0]; steer.Text != "" || len(steer.Images) != 1 || string(steer.Images[0].Data) != "crop" {
		t.Fatalf("steer = %+v", steer)
	}

	if state.Objective != "build this" || len(state.ObjectiveImages) != 1 {
		t.Fatalf("objective %q with %d image(s)", state.Objective, len(state.ObjectiveImages))
	}

	entries = append(entries, entry(4, KindCompaction, Compaction{Text: "summary", Images: []protocol.Image{mockup}}))

	if state, err = Build(entries); err != nil {
		t.Fatal(err)
	}

	if summary := state.Messages[0].Content[0]; summary.Text != "summary" || len(summary.Images) != 1 || string(summary.Images[0].Data) != "mockup" {
		t.Fatalf("after the compaction = %+v", summary)
	}
}
