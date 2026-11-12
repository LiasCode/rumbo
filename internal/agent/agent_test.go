package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"rumbo/internal/ambient"
	"rumbo/internal/store"
	"rumbo/internal/tool"
	"rumbo/internal/transcript"
	"rumbo/internal/verify"
	"rumbo/protocol"
)

// scripted is a Provider that answers with fixed messages and keeps the
// requests it received. failures are returned first, one per call; cut marks
// the answers, by position, that the output limit cut short.
type scripted struct {
	answers  []protocol.Message
	failures []error
	cut      map[int]bool
	requests []protocol.CompleteParams
	answered int
}

func (s *scripted) Complete(ctx context.Context, params protocol.CompleteParams, onDelta func(string)) (protocol.CompleteResult, error) {
	s.requests = append(s.requests, params)

	if len(s.failures) > 0 {
		failure := s.failures[0]
		s.failures = s.failures[1:]

		return protocol.CompleteResult{}, failure
	}

	if s.answered >= len(s.answers) {
		<-ctx.Done()
		return protocol.CompleteResult{}, ctx.Err()
	}

	answer := s.answers[s.answered]
	stop := protocol.StopEnd

	for _, block := range answer.Content {
		if block.Type == protocol.BlockToolCall {
			stop = protocol.StopToolCalls
		}
	}

	if s.cut[s.answered] {
		stop = protocol.StopMaxTokens
	}

	s.answered++

	return protocol.CompleteResult{Message: answer, Stop: stop}, nil
}

func (s *scripted) Close() error {
	return nil
}

type silent struct {
	verified []bool
	notices  []string
	shown    string
}

func (*silent) ContextUpdated([]string)             {}
func (s *silent) Text(delta string)                 { s.shown += delta }
func (*silent) ToolStarted(string, json.RawMessage) {}
func (*silent) ToolFinished(string, string, bool)   {}
func (*silent) TurnFinished(protocol.Usage)         {}

func (s *silent) Notice(text string) {
	s.notices = append(s.notices, text)
}

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

	// The last request was sent before the final answer and the notes that
	// close the run were recorded.
	state, err := transcript.Build(entries[:len(entries)-3])

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

	if kinds(added) != "user assistant note" {
		t.Fatalf("second run added %q, want only user, assistant, and the closing note", kinds(added))
	}
}

func kinds(entries []store.Entry) string {
	names := make([]string, len(entries))

	for index, entry := range entries {
		names[index] = entry.Kind
	}

	return strings.Join(names, " ")
}

// setup returns an agent on a fresh session, with no checks and no ambient
// context, so a test sees only what it is about.
func setup(t *testing.T, model *scripted) (*Agent, store.Store, store.Session, *silent) {
	t.Helper()

	database, err := store.Open(filepath.Join(t.TempDir(), "test.db"))

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { database.Close() })

	session, err := database.CreateSession(t.TempDir(), "test")

	if err != nil {
		t.Fatal(err)
	}

	observer := &silent{}

	worker := &Agent{
		Store:      database,
		Provider:   model,
		Observer:   observer,
		Tools:      tool.Defaults(),
		Model:      "test",
		RetryWaits: []time.Duration{time.Millisecond, time.Millisecond},
	}

	return worker, database, session, observer
}

func notes(t *testing.T, database store.Store, session store.Session) string {
	t.Helper()

	entries, err := database.Entries(session.ID)

	if err != nil {
		t.Fatal(err)
	}

	var types []string

	for _, entry := range entries {
		if entry.Kind == transcript.KindNote {
			var note transcript.Note
			json.Unmarshal(entry.Payload, &note)
			types = append(types, note.Type)
		}
	}

	return strings.Join(types, " ")
}

func TestTransientFailuresAreRetried(t *testing.T) {
	overloaded := protocol.Transient(errors.New("overloaded"))
	model := &scripted{failures: []error{overloaded, overloaded}, answers: []protocol.Message{say(text("Hi."))}}
	worker, database, session, observer := setup(t, model)

	if err := worker.Run(context.Background(), session, "say hi"); err != nil {
		t.Fatal(err)
	}

	if len(model.requests) != 3 || len(observer.notices) != 2 {
		t.Fatalf("requests = %d, notices = %v", len(model.requests), observer.notices)
	}

	// This provider sends no deltas, and the answer is shown all the same.
	if observer.shown != "Hi." {
		t.Fatalf("shown = %q, want the text of the message", observer.shown)
	}

	if got := notes(t, database, session); got != "retry retry done" {
		t.Fatalf("notes = %q", got)
	}

	// The waits run out, and a failure that is not transient is never retried.
	model.failures = []error{overloaded, overloaded, overloaded}

	if err := worker.Run(context.Background(), session, "again"); !protocol.IsTransient(err) {
		t.Fatalf("err = %v, want the transient failure after the retries", err)
	}

	sent := len(model.requests)
	model.failures = []error{errors.New("invalid api key")}

	if err := worker.Run(context.Background(), session, "again"); err == nil || len(model.requests) != sent+1 {
		t.Fatalf("a permanent failure was retried: err = %v, requests = %d", err, len(model.requests)-sent)
	}
}

func TestCutResponsesContinue(t *testing.T) {
	model := &scripted{
		cut: map[int]bool{0: true, 1: true},
		answers: []protocol.Message{
			say(text("The answer is")),
			say(write("call-1", "big.txt", "partial")),
			say(text(" 42.")),
		},
	}

	worker, database, session, _ := setup(t, model)

	if err := worker.Run(context.Background(), session, "answer"); err != nil {
		t.Fatal(err)
	}

	second := model.requests[1].Messages
	third := model.requests[2].Messages

	if got := second[len(second)-1].Content[0].Text; got != cutText {
		t.Fatalf("after a cut text the model was told %q", got)
	}

	result := third[len(third)-1].Content[0]

	if result.Type != protocol.BlockToolResult || !result.IsError || result.Text != cutCall {
		t.Fatalf("a cut tool call should get an error result, got %+v", result)
	}

	if _, err := os.Stat(filepath.Join(session.Dir, "big.txt")); err == nil {
		t.Fatal("a tool call from a cut response was executed")
	}

	if got := notes(t, database, session); got != "done" {
		t.Fatalf("notes = %q", got)
	}
}

func TestRunStopsAtMaxTurns(t *testing.T) {
	model := &scripted{}

	for index := range 10 {
		model.answers = append(model.answers, say(write(fmt.Sprintf("call-%d", index), "loop.txt", "again")))
	}

	worker, database, session, _ := setup(t, model)
	worker.MaxTurns = 3

	err := worker.Run(context.Background(), session, "loop")

	if err == nil || len(model.requests) != 3 {
		t.Fatalf("err = %v, requests = %d, want a stop after 3", err, len(model.requests))
	}

	if got := notes(t, database, session); got != "failed" {
		t.Fatalf("notes = %q", got)
	}
}

// A run cancelled in the middle of a tool call is resumed: the call that
// never finished is closed with an error result, and the model is told.
func TestResumeAfterCancelClosesTheOpenCall(t *testing.T) {
	sleep, _ := json.Marshal(map[string]any{"command": "sleep 30"})
	model := &scripted{answers: []protocol.Message{
		say(protocol.Block{Type: protocol.BlockToolCall, ID: "call-1", Name: "bash", Args: sleep}),
	}}

	worker, database, session, _ := setup(t, model)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if err := worker.Run(ctx, session, "wait"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the cancel", err)
	}

	model.answers = append(model.answers, say(text("Stopped waiting.")))

	if err := worker.Resume(context.Background(), session); err != nil {
		t.Fatal(err)
	}

	sent := model.requests[1].Messages
	last := sent[len(sent)-1].Content

	if len(last) != 2 || last[0].Type != protocol.BlockToolResult || last[0].ID != "call-1" || !last[0].IsError || last[1].Text != resumed {
		t.Fatalf("resumed request should end with the closed call and the harness message, got %+v", last)
	}

	if got := notes(t, database, session); got != "cancelled done" {
		t.Fatalf("notes = %q", got)
	}
}

func TestResume(t *testing.T) {
	model := &scripted{failures: []error{errors.New("connection reset")}}
	worker, database, session, _ := setup(t, model)
	worker.Checks = []string{"test -f done.txt"}
	worker.MaxVerifyRounds = 1

	if err := worker.Run(context.Background(), session, "create done.txt"); err == nil {
		t.Fatal("the run should have failed")
	}

	model.answers = []protocol.Message{say(text("Nothing was needed.")), say(write("call-1", "done.txt", "x")), say(text("Created."))}

	if err := worker.Resume(context.Background(), session); err != nil {
		t.Fatal(err)
	}

	resumedRequest := model.requests[1].Messages
	last := resumedRequest[len(resumedRequest)-1].Content

	if last[0].Text != "create done.txt" || last[1].Text != resumed {
		t.Fatalf("resumed request should end with the objective and the harness message, got %+v", last)
	}

	// The interrupted run may have changed the project, so the checks ran
	// even though the resumed model first stopped without touching anything,
	// and their feedback restated the original objective.
	feedbackRequest := model.requests[2].Messages
	told := feedbackRequest[len(feedbackRequest)-1].Content[0].Text

	if !strings.Contains(told, "test -f done.txt") || !strings.Contains(told, "create done.txt") {
		t.Fatalf("check feedback = %q", told)
	}

	if got := notes(t, database, session); got != "failed checks_passed done" {
		t.Fatalf("notes = %q", got)
	}

	if err := worker.Resume(context.Background(), session); err == nil || !strings.Contains(err.Error(), "nothing to resume") {
		t.Fatalf("resuming a finished run: err = %v", err)
	}
}

// isPrefix reports whether shorter is the start of longer, allowing the last
// message of shorter to have grown by appended blocks.
func isPrefix(shorter, longer []protocol.Message) bool {
	a, _ := json.Marshal(shorter)
	b, _ := json.Marshal(longer)

	return strings.HasPrefix(string(b), strings.TrimSuffix(string(a), "]}]"))
}
