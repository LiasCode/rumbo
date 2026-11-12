package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rumbo/internal/ambient"
	"rumbo/internal/store"
	"rumbo/internal/tool"
	"rumbo/internal/transcript"
	"rumbo/protocol"
)

// scripted is a Provider that answers with fixed messages and keeps the
// requests it received. failures are returned first, one per call; cut marks
// the answers, by position, that the output limit cut short.
type scripted struct {
	answers  []protocol.Message
	failures []error
	cut      map[int]bool
	usage    protocol.Usage
	window   int64
	// failAt makes the request with that number, counting from zero, fail.
	failAt   map[int]error
	requests []protocol.CompleteParams
	answered int
}

func (s *scripted) Complete(ctx context.Context, params protocol.CompleteParams, onDelta func(string)) (protocol.CompleteResult, error) {
	s.requests = append(s.requests, params)

	if failure, found := s.failAt[len(s.requests)-1]; found {
		return protocol.CompleteResult{}, failure
	}

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

	return protocol.CompleteResult{Message: answer, Stop: stop, Usage: s.usage, ContextWindow: s.window}, nil
}

func (s *scripted) Close() error {
	return nil
}

func (s *scripted) Description() protocol.Description {
	return protocol.Description{}
}

func (s *scripted) Invoke(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("no functions")
}

type silent struct {
	notices []string
	shown   string
	context int64
	window  int64
}

func (*silent) ContextUpdated([]string)             {}
func (s *silent) Text(delta string)                 { s.shown += delta }
func (*silent) ToolStarted(string, json.RawMessage) {}
func (*silent) ToolFinished(string, string, bool)   {}
func (s *silent) TurnFinished(_ protocol.Usage, context, window int64) {
	if context > 0 {
		s.context, s.window = context, window
	}
}

func (s *silent) Notice(text string) {
	s.notices = append(s.notices, text)
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

// A run with a tool call: what the model is sent, that each request extends
// the one before, and that the stored entries rebuild it exactly.
func TestRunRecordsAReproducibleHistory(t *testing.T) {
	dir := t.TempDir()
	database, err := store.Open(filepath.Join(t.TempDir(), "test.db"))

	if err != nil {
		t.Fatal(err)
	}

	defer database.Close()

	model := &scripted{answers: []protocol.Message{
		say(write("call-1", "state.txt", "ok")),
		say(text("Done.")),
	}}

	worker := &Agent{
		Store:        database,
		Provider:     model,
		Observer:     &silent{},
		Tools:        tool.Defaults(),
		Collectors:   []ambient.Collector{ambient.Func("note", func(context.Context, string) string { return "hello" })},
		ProviderName: "scripted",
		Model:        "test",
	}

	session, err := database.CreateSession(dir, dir, "test")

	if err != nil {
		t.Fatal(err)
	}

	if err := worker.Run(context.Background(), session, "write ok to state.txt"); err != nil {
		t.Fatal(err)
	}

	if content, _ := os.ReadFile(filepath.Join(dir, "state.txt")); string(content) != "ok" {
		t.Fatalf("state.txt = %q, want ok", content)
	}

	if len(model.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(model.requests))
	}

	first := model.requests[0].Messages

	if len(first) != 1 || len(first[0].Content) != 2 || !strings.Contains(first[0].Content[0].Text, `<context name="note">`) {
		t.Fatalf("first request should be one user message with context then objective, got %+v", first)
	}

	// Every request is a prefix of the next one: history is append-only.
	if !isPrefix(model.requests[0].Messages, model.requests[1].Messages) {
		t.Fatal("the second request does not extend the first")
	}

	// The stored entries alone rebuild the last request sent.
	entries, err := database.Entries(session.ID)

	if err != nil {
		t.Fatal(err)
	}

	if got := kinds(entries); got != "system context user assistant tool_result assistant note" {
		t.Fatalf("entries = %q", got)
	}

	// The last request was sent before the final answer and the closing note
	// were recorded.
	state, err := transcript.Build(entries[:len(entries)-2])

	if err != nil {
		t.Fatal(err)
	}

	got, _ := json.Marshal(state.Messages)
	want, _ := json.Marshal(model.requests[1].Messages)

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

	if added := after[len(entries):]; kinds(added) != "user assistant note" {
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

// setup returns an agent on a fresh session with no ambient context, so a
// test sees only what it is about.
func setup(t *testing.T, model *scripted) (*Agent, store.Store, store.Session, *silent) {
	t.Helper()

	database, err := store.Open(filepath.Join(t.TempDir(), "test.db"))

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { database.Close() })

	dir := t.TempDir()
	session, err := database.CreateSession(dir, dir, "test")

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

	if err := worker.Run(context.Background(), session, "create done.txt"); err == nil {
		t.Fatal("the run should have failed")
	}

	model.answers = []protocol.Message{say(write("call-1", "done.txt", "x")), say(text("Created."))}

	if err := worker.Resume(context.Background(), session); err != nil {
		t.Fatal(err)
	}

	resumedRequest := model.requests[1].Messages
	last := resumedRequest[len(resumedRequest)-1].Content

	if last[0].Text != "create done.txt" || last[1].Text != resumed {
		t.Fatalf("resumed request should end with the objective and the harness message, got %+v", last)
	}

	if got := notes(t, database, session); got != "failed done" {
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

// When the context reaches the limit, the model is asked for a summary on
// the same conversation, and the next request starts from that summary alone.
func TestCompactionReplacesTheConversation(t *testing.T) {
	model := &scripted{
		usage: protocol.Usage{Input: 80, CacheRead: 30, Output: 10},
		answers: []protocol.Message{
			say(write("call-1", "a.txt", "one")),
			say(text("Thinking aloud. <summary>Wrote a.txt with one.</summary>")),
			say(text("Done.")),
		},
	}

	worker, database, session, observer := setup(t, model)
	worker.Compaction = Compaction{Tokens: 100}
	worker.Collectors = []ambient.Collector{ambient.Func("note", func(context.Context, string) string { return "hello" })}

	if err := worker.Run(context.Background(), session, "write one to a.txt"); err != nil {
		t.Fatal(err)
	}

	if len(model.requests) != 3 {
		t.Fatalf("requests = %d, want the turn, the summary, and the turn after it", len(model.requests))
	}

	// The summary request is the conversation so far plus the instruction,
	// so it reuses the cached prefix.
	asked := model.requests[1].Messages
	tail := asked[len(asked)-1].Content

	if len(asked) != 3 || tail[0].Type != protocol.BlockToolResult || tail[len(tail)-1].Text != summarize {
		t.Fatalf("summary request should end with the tool result and the instruction, got %+v", tail)
	}

	// The request after it carries the context again, the summary, and the
	// objective, and nothing from before.
	after := model.requests[2].Messages

	if len(after) != 1 || len(after[0].Content) != 2 {
		t.Fatalf("request after compaction = %+v, want one user message with context and summary", after)
	}

	context, told := after[0].Content[0].Text, after[0].Content[1].Text

	if !strings.Contains(context, `<context name="note">`) || !strings.Contains(told, "Wrote a.txt with one.") ||
		!strings.Contains(told, "write one to a.txt") || strings.Contains(told, "Thinking aloud") {
		t.Fatalf("after compaction the model saw:\n%s\n%s", context, told)
	}

	if observer.shown != "Done." {
		t.Fatalf("shown = %q; the summary is not an answer and must not be shown", observer.shown)
	}

	entries, _ := database.Entries(session.ID)

	if got := kinds(entries); got != "system context user assistant tool_result compaction assistant note" {
		t.Fatalf("entries = %q", got)
	}
}

func TestCompactionFailsWithoutASummary(t *testing.T) {
	model := &scripted{
		usage: protocol.Usage{Input: 500},
		answers: []protocol.Message{
			say(write("call-1", "a.txt", "one")),
			say(write("call-2", "b.txt", "instead of a summary")),
		},
	}

	worker, _, session, _ := setup(t, model)
	worker.Compaction = Compaction{Tokens: 100}

	err := worker.Run(context.Background(), session, "write")

	if err == nil || !strings.Contains(err.Error(), "could not summarize") {
		t.Fatalf("err = %v", err)
	}

	if _, err := os.Stat(filepath.Join(session.Dir, "b.txt")); err == nil {
		t.Fatal("a tool call made instead of the summary was executed")
	}
}

// A compaction can be asked for at any size, between runs.
func TestCompactOnRequest(t *testing.T) {
	model := &scripted{
		usage:   protocol.Usage{Input: 40, Output: 5},
		answers: []protocol.Message{say(text("The capital is Paris."))},
	}

	worker, database, session, _ := setup(t, model)

	if _, err := worker.Compact(context.Background(), session); err == nil {
		t.Fatal("an empty session was compacted")
	}

	if err := worker.Run(context.Background(), session, "what is the capital of France?"); err != nil {
		t.Fatal(err)
	}

	model.answers = append(model.answers, say(text("<summary>Asked for the capital of France; it is Paris.</summary>")), say(text("Madrid.")))

	before, err := worker.Compact(context.Background(), session)

	if err != nil || before != 45 {
		t.Fatalf("before = %d, err = %v", before, err)
	}

	if err := worker.Run(context.Background(), session, "and of Spain?"); err != nil {
		t.Fatal(err)
	}

	sent := model.requests[2].Messages

	if len(sent) != 1 || !strings.Contains(sent[0].Content[0].Text, "it is Paris") || sent[0].Content[1].Text != "and of Spain?" {
		t.Fatalf("the request after the compaction = %+v", sent)
	}

	entries, _ := database.Entries(session.ID)

	if got := kinds(entries); got != "system user assistant note compaction user assistant note" {
		t.Fatalf("entries = %q", got)
	}
}

func TestCompactionLimitFollowsTheModelsContext(t *testing.T) {
	for _, test := range []struct {
		policy Compaction
		window int64
		want   int64
	}{
		{Compaction{}, 0, 150_000},                // nothing known: the fixed limit
		{Compaction{}, 1_000_000, 150_000},        // a very large context is not filled
		{Compaction{}, 32_000, 25_600},            // a small one is used to 80%
		{Compaction{Percent: 50}, 32_000, 16_000}, // a share the user chose
		{Compaction{Percent: 90}, 1_000_000, 900_000},
		{Compaction{Percent: 50}, 0, 150_000}, // a share of an unknown size falls back
		{Compaction{Tokens: 5_000}, 32_000, 5_000},
		{Compaction{Never: true}, 32_000, 0},
	} {
		if got := test.policy.Limit(test.window); got != test.want {
			t.Errorf("%+v with a context of %d: limit %d, want %d", test.policy, test.window, got, test.want)
		}
	}
}

// The provider says the model's context is small, and the conversation is
// summarized before it fills, with no setting touched.
func TestCompactionUsesTheWindowTheProviderReports(t *testing.T) {
	model := &scripted{
		usage:  protocol.Usage{Input: 900},
		window: 1_000,
		answers: []protocol.Message{
			say(write("call-1", "a.txt", "one")),
			say(text("<summary>Wrote a.txt.</summary>")),
			say(text("Done.")),
		},
	}

	worker, database, session, observer := setup(t, model)

	if err := worker.Run(context.Background(), session, "write"); err != nil {
		t.Fatal(err)
	}

	entries, _ := database.Entries(session.ID)

	if got := kinds(entries); got != "system user assistant tool_result compaction assistant note" {
		t.Fatalf("entries = %q", got)
	}

	if observer.window != 1_000 || observer.context != 900 {
		t.Fatalf("the front end was told a context of %d in a window of %d", observer.context, observer.window)
	}
}

// A request that does not fit is recovered from: the conversation is
// summarized without the step that tipped it over, and the model is told.
func TestOverflowIsRecoveredByDroppingTheLastStep(t *testing.T) {
	tooLong := protocol.Overflow(errors.New("prompt is too long"))

	model := &scripted{answers: []protocol.Message{
		say(write("call-1", "a.txt", "one")),
		say(command("call-2", "echo a very long log")),
		say(text("<summary>Wrote a.txt, then looked at a log.</summary>")),
		say(text("Done.")),
	}}

	worker, database, session, observer := setup(t, model)

	// The third request, which carries the log, is the one that overflows.
	model.failAt = map[int]error{2: tooLong}

	if err := worker.Run(context.Background(), session, "inspect the log"); err != nil {
		t.Fatal(err)
	}

	// The summary was asked for on the conversation without the log step.
	asked := model.requests[3].Messages

	for _, message := range asked {
		for _, block := range message.Content {
			if block.ID == "call-2" {
				t.Fatal("the step that did not fit was sent again to be summarized")
			}
		}
	}

	if tail := asked[len(asked)-1].Content; tail[len(tail)-1].Text != summarize {
		t.Fatalf("the summary request ends with %+v", tail)
	}

	after := model.requests[4].Messages

	if len(after) != 1 || !strings.Contains(after[0].Content[0].Text, "looked at a log") || !strings.Contains(after[0].Content[0].Text, "in smaller pieces") {
		t.Fatalf("after the recovery the model saw %+v", after)
	}

	if !strings.Contains(strings.Join(observer.notices, "\n"), "did not fit") {
		t.Fatalf("notices = %v", observer.notices)
	}

	if got := notes(t, database, session); got != "done" {
		t.Fatalf("notes = %q", got)
	}

	// When the summary does not fit either, the run fails instead of looping.
	model.answers = append(model.answers, say(write("call-3", "b.txt", "two")))
	model.failAt = map[int]error{6: tooLong, 7: tooLong}

	if err := worker.Run(context.Background(), session, "again"); err == nil || !strings.Contains(err.Error(), "could not summarize") {
		t.Fatalf("a second overflow in a row: err = %v", err)
	}
}

func command(id, line string) protocol.Block {
	args, _ := json.Marshal(map[string]string{"command": line})

	return protocol.Block{Type: protocol.BlockToolCall, ID: id, Name: "bash", Args: args}
}
