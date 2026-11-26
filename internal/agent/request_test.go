package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/LiasCode/rumbo/internal/transcript"
	"github.com/LiasCode/rumbo/protocol"
)

// recorder keeps, as they were sent, the requests a provider answered.
type recorder struct {
	*scripted
	sent [][]byte
}

func (r *recorder) Complete(ctx context.Context, params protocol.CompleteParams, onDelta func(string)) (protocol.CompleteResult, error) {
	result, err := r.scripted.Complete(ctx, params, onDelta)

	if err == nil {
		raw, _ := json.Marshal(params)
		r.sent = append(r.sent, raw)
	}

	return result, err
}

// Every request a session's runs sent is rebuilt from its entries, byte for
// byte: after a tool call, a retry, a cut response, a steer, a summary made
// because a request did not fit, a summary asked for, and another run.
func TestEveryRequestIsReproduced(t *testing.T) {
	model := &scripted{
		answers: []protocol.Message{
			say(write("call-1", "a.txt", "one")),
			say(text("Half")),
			say(command("call-2", briefPause())),
			say(text("<summary>Wrote a.txt and checked it.</summary>")),
			say(text("Done.")),
			say(text("<summary>All done.</summary>")),
			say(text("Fine.")),
		},
		cut: map[int]bool{1: true},
		failAt: map[int]error{
			1: protocol.Transient(errors.New("overloaded")),
			4: protocol.Overflow(errors.New("prompt is too long")),
		},
	}

	worker, database, session, _ := setup(t, model)
	sent := &recorder{scripted: model}
	worker.Provider = sent
	worker.ProviderName, worker.MaxTokens, worker.Version = "scripted", 4096, "test-version"
	worker.Options = json.RawMessage(`{"effort":"high"}`)
	worker.Inbox = &told{model: model, said: map[int][]string{4: {"also check b"}}}
	ctx := context.Background()

	if err := worker.Run(ctx, session, "write one to a.txt"); err != nil {
		t.Fatal(err)
	}

	if _, err := worker.Compact(ctx, session); err != nil {
		t.Fatal(err)
	}

	if err := worker.Run(ctx, session, "anything else?"); err != nil {
		t.Fatal(err)
	}

	entries, err := database.Entries(session.ID)

	if err != nil {
		t.Fatal(err)
	}

	if turns := transcript.Summarize(entries).Turns; turns != len(sent.sent) || turns != 7 {
		t.Fatalf("%d turns recorded, %d requests answered, want 7 of each", turns, len(sent.sent))
	}

	for turn := 1; turn <= len(sent.sent); turn++ {
		params, recorded, err := Request(entries, turn)

		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}

		if got, _ := json.Marshal(params); string(got) != string(sent.sent[turn-1]) {
			t.Errorf("turn %d is not the request sent\n got: %s\nwant: %s", turn, got, sent.sent[turn-1])
		}

		if recorded.MaxTokens != 4096 || recorded.Version != "test-version" {
			t.Errorf("turn %d recorded %+v of its request", turn, recorded)
		}
	}

	for _, turn := range []int{0, 8} {
		if _, _, err := Request(entries, turn); err == nil {
			t.Errorf("turn %d of 7 was rebuilt", turn)
		}
	}

	// A tool result says how long the tool took; it is never sent.
	for _, entry := range entries {
		var result transcript.ToolResult
		json.Unmarshal(entry.Payload, &result)

		if entry.Kind == transcript.KindToolResult && result.Name == shellTool && result.Duration < 10 {
			t.Errorf("the command slept 10ms, and took %dms", result.Duration)
		}
	}
}
