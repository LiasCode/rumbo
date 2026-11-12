package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"rumbo/internal/ambient"
	"rumbo/internal/store"
	"rumbo/internal/tool"
	"rumbo/internal/transcript"
	"rumbo/internal/verify"
	"rumbo/protocol"
)

// scripted is a Provider that answers with fixed messages and keeps the
// requests it received.
type scripted struct {
	answers  []protocol.Message
	requests []protocol.CompleteParams
}

func (s *scripted) Complete(_ context.Context, params protocol.CompleteParams, onDelta func(string)) (protocol.CompleteResult, error) {
	s.requests = append(s.requests, params)
	answer := s.answers[len(s.requests)-1]
	stop := protocol.StopEnd

	for _, block := range answer.Content {
		if block.Type == protocol.BlockToolCall {
			stop = protocol.StopToolCalls
		}
	}

	return protocol.CompleteResult{Message: answer, Stop: stop}, nil
}

func (s *scripted) Close() error {
	return nil
}

type silent struct {
	verified []bool
}

func (*silent) ContextUpdated([]string)             {}
func (*silent) Text(string)                         {}
func (*silent) ToolStarted(string, json.RawMessage) {}
func (*silent) ToolFinished(string, string, bool)   {}
func (*silent) TurnFinished(protocol.Usage)         {}

func (s *silent) Verified(_ []string, result verify.Result) {
	s.verified = append(s.verified, result.Passed)
}

func write(id, path, content string) protocol.Block {
	args, _ := json.Marshal(map[string]string{"path": path, "content": content})

	return protocol.Block{Type: protocol.BlockToolCall, ID: id, Name: "write", Args: args}
}

func say(blocks ...protocol.Block) protocol.Message {
	return protocol.Message{Role: protocol.RoleAssistant, Content: blocks}
}

func text(value string) protocol.Block {
	return protocol.Block{Type: protocol.BlockText, Text: value}
}

// The agent stops after writing a file that fails the check; the harness
// sends the failure back, the agent fixes it, and only then the run ends.
func TestRunVerifiesBeforeFinishing(t *testing.T) {
	dir := t.TempDir()
	database, err := store.Open(filepath.Join(t.TempDir(), "test.db"))

	if err != nil {
		t.Fatal(err)
	}

	defer database.Close()

	model := &scripted{answers: []protocol.Message{
		say(write("call-1", "state.txt", "bad")),
		say(text("Done.")),
		say(write("call-2", "state.txt", "ok")),
		say(text("Fixed.")),
	}}

	observer := &silent{}

	worker := &Agent{
		Store:           database,
		Provider:        model,
		Observer:        observer,
		Tools:           tool.Defaults(),
		Collectors:      []ambient.Collector{ambient.Func("note", func(context.Context, string) string { return "hello" })},
		ProviderName:    "scripted",
		Model:           "test",
		Checks:          []string{`grep -q ok state.txt`},
		MaxVerifyRounds: 3,
	}

	session, err := database.CreateSession(dir, "test")

	if err != nil {
		t.Fatal(err)
	}

	if err := worker.Run(context.Background(), session, "write ok to state.txt"); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(observer.verified, []bool{false, true}) {
		t.Fatalf("verification outcomes = %v, want [false true]", observer.verified)
	}

	if content, _ := os.ReadFile(filepath.Join(dir, "state.txt")); string(content) != "ok" {
		t.Fatalf("state.txt = %q, want ok", content)
	}

	if len(model.requests) != 4 {
		t.Fatalf("requests = %d, want 4", len(model.requests))
	}

	first := model.requests[0].Messages

	if len(first) != 1 || len(first[0].Content) != 2 || !strings.Contains(first[0].Content[0].Text, `<context name="note">`) {
		t.Fatalf("first request should be one user message with context then objective, got %+v", first)
	}

	feedback := model.requests[2].Messages
	last := feedback[len(feedback)-1].Content

	if !strings.Contains(last[len(last)-1].Text, "write ok to state.txt") {
		t.Fatalf("check feedback should restate the objective, got %q", last[len(last)-1].Text)
	}

	// Every request is a prefix of the next one: history is append-only.
	for index := 1; index < len(model.requests); index++ {
		previous, current := model.requests[index-1].Messages, model.requests[index].Messages

		if !isPrefix(previous, current) {
			t.Fatalf("request %d does not extend request %d", index, index-1)
		}
	}

	// The stored entries alone rebuild the last request sent.
	entries, err := database.Entries(session.ID)

	if err != nil {
		t.Fatal(err)
	}

	state, err := transcript.Build(entries[:len(entries)-1])

	if err != nil {
		t.Fatal(err)
	}

	got, _ := json.Marshal(state.Messages)
	want, _ := json.Marshal(model.requests[3].Messages)

	if string(got) != string(want) {
		t.Fatalf("replayed request differs from the one sent\n got: %s\nwant: %s", got, want)
	}

	// A second run in the same session adds no system or context entries,
	// because neither changed.
	model.answers = append(model.answers, say(text("Nothing to do.")))

	if err := worker.Run(context.Background(), session, "anything else?"); err != nil {
		t.Fatal(err)
	}

	after, _ := database.Entries(session.ID)
	added := after[len(entries):]

	if len(added) != 2 || added[0].Kind != transcript.KindUser || added[1].Kind != transcript.KindAssistant {
		t.Fatalf("second run should add only user and assistant entries, got %d", len(added))
	}
}

// isPrefix reports whether shorter is the start of longer, allowing the last
// message of shorter to have grown by appended blocks.
func isPrefix(shorter, longer []protocol.Message) bool {
	a, _ := json.Marshal(shorter)
	b, _ := json.Marshal(longer)

	return strings.HasPrefix(string(b), strings.TrimSuffix(string(a), "]}]"))
}
