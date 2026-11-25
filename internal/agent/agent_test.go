package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LiasCode/rumbo/internal/ambient"
	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/internal/tool"
	"github.com/LiasCode/rumbo/internal/transcript"
	"github.com/LiasCode/rumbo/protocol"
)

// scripted is a Provider that answers with fixed messages and keeps the
// requests it received. failures are returned first, one per call; cut marks
// the answers, by position, that the output limit cut short, and usages
// gives some of them a usage of their own instead of usage.
type scripted struct {
	answers  []protocol.Message
	failures []error
	cut      map[int]bool
	usage    protocol.Usage
	usages   map[int]protocol.Usage
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

	usage, found := s.usages[s.answered]

	if !found {
		usage = s.usage
	}

	s.answered++

	return protocol.CompleteResult{Message: answer, Stop: stop, Usage: usage, ContextWindow: s.window}, nil
}

func (s *scripted) Close() error {
	return nil
}

func (s *scripted) Description() protocol.Description {
	return protocol.Description{}
}

func (s *scripted) Invoke(context.Context, string, json.RawMessage, protocol.Dialog) (json.RawMessage, error) {
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
	database := store.Open(filepath.Join(t.TempDir(), "config.yaml"), t.TempDir())

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

	session, err := database.CreateSession(dir, "test")

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

	database := store.Open(filepath.Join(t.TempDir(), "config.yaml"), t.TempDir())

	dir := t.TempDir()
	session, err := database.CreateSession(dir, "test")

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
		{Compaction{}, 1_000_000, 800_000},        // a very large context is used to 80% too
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

// told is an Inbox where the user speaks once the model has received a
// given number of requests.
type told struct {
	model *scripted
	said  map[int][]string
}

func (t *told) Take(record func(string, []string) error) error {
	asked := len(t.model.requests)
	texts := t.said[asked]
	delete(t.said, asked)

	for _, text := range texts {
		if err := record(text, nil); err != nil {
			return err
		}
	}

	return nil
}

func lastText(message protocol.Message) string {
	return message.Content[len(message.Content)-1].Text
}

// What the user says while a tool runs reaches the model with the tool's
// result, and the run keeps the objective it had.
func TestSteerReachesTheNextTurn(t *testing.T) {
	model := &scripted{answers: []protocol.Message{
		say(write("call-1", "a.txt", "one")),
		say(text("Done.")),
	}}

	worker, database, session, _ := setup(t, model)
	worker.Inbox = &told{model: model, said: map[int][]string{1: {"name it b.txt instead"}}}

	if err := worker.Run(context.Background(), session, "write a file"); err != nil {
		t.Fatal(err)
	}

	second := model.requests[1].Messages
	last := second[len(second)-1]

	if last.Role != protocol.RoleUser || len(last.Content) != 2 || last.Content[0].Type != protocol.BlockToolResult || last.Content[1].Text != "name it b.txt instead" {
		t.Fatalf("the second request ends with %+v", last)
	}

	entries, _ := database.Entries(session.ID)

	if got := kinds(entries); got != "system user assistant tool_result steer assistant note" {
		t.Fatalf("entries = %q", got)
	}

	if state, _ := transcript.Build(entries); state.Objective != "write a file" || state.Unfinished {
		t.Fatalf("after the run: objective %q, unfinished %t", state.Objective, state.Unfinished)
	}
}

// What the user says as the model finishes keeps the run going, so the
// model reads it before the run ends.
func TestSteerAsTheModelFinishesKeepsTheRunGoing(t *testing.T) {
	model := &scripted{answers: []protocol.Message{
		say(text("Done.")),
		say(text("Added the tests too.")),
	}}

	worker, database, session, _ := setup(t, model)
	worker.Inbox = &told{model: model, said: map[int][]string{1: {"add tests as well"}}}

	if err := worker.Run(context.Background(), session, "fix it"); err != nil {
		t.Fatal(err)
	}

	if len(model.requests) != 2 || lastText(model.requests[1].Messages[len(model.requests[1].Messages)-1]) != "add tests as well" {
		t.Fatalf("requests = %d, the last one = %+v", len(model.requests), model.requests[len(model.requests)-1].Messages)
	}

	if got := notes(t, database, session); got != "done" {
		t.Fatalf("notes = %q", got)
	}
}

// A request that does not fit drops its last step, but not what the user
// said during it: that is said again after the summary.
func TestSteerSurvivesAnOverflow(t *testing.T) {
	tooLong := protocol.Overflow(errors.New("prompt is too long"))

	model := &scripted{answers: []protocol.Message{
		say(write("call-1", "a.txt", "one")),
		say(command("call-2", "echo a very long log")),
		say(text("<summary>Wrote a.txt, then looked at a log.</summary>")),
		say(text("Done.")),
	}}

	worker, _, session, _ := setup(t, model)
	worker.Inbox = &told{model: model, said: map[int][]string{2: {"keep the log short"}}}
	model.failAt = map[int]error{2: tooLong}

	if err := worker.Run(context.Background(), session, "inspect the log"); err != nil {
		t.Fatal(err)
	}

	// The request that overflowed carried it, the summary was asked without
	// it, and the request after the summary carries it again.
	if lastText(model.requests[2].Messages[len(model.requests[2].Messages)-1]) != "keep the log short" {
		t.Fatalf("the overflowing request ends with %+v", model.requests[2].Messages)
	}

	after := model.requests[4].Messages

	if len(after) != 1 || !strings.Contains(after[0].Content[0].Text, "looked at a log") || lastText(after[0]) != "keep the log short" {
		t.Fatalf("after the recovery the model saw %+v", after)
	}
}

// script is an Inbox that acts once the model has received a given number
// of requests: it tells the run something, or does something to it. It runs
// in the loop, between turns.
type script struct {
	model *scripted
	at    map[int]func(record func(string) error) error
}

func (s *script) Take(record func(string, []string) error) error {
	asked := len(s.model.requests)
	act := s.at[asked]
	delete(s.at, asked)

	if act == nil {
		return nil
	}

	return act(func(text string) error { return record(text, nil) })
}

// changing is a collector whose section is whatever version says.
type changing struct {
	version string
}

func (c *changing) Name() string { return "versioned" }

func (c *changing) Collect(context.Context, string) string { return c.version }

// extends says why next does not extend prev, or nothing when it does: the
// system prompt, the tools, the settings, and every message before must be
// the same, so a provider can cache all of it.
func extends(prev, next protocol.CompleteParams) string {
	prevTools, _ := json.Marshal(prev.Tools)
	nextTools, _ := json.Marshal(next.Tools)

	switch {
	case next.System != prev.System:
		return "the system prompt changed"
	case !bytes.Equal(prevTools, nextTools):
		return "the tools changed"
	case next.Model != prev.Model || next.MaxTokens != prev.MaxTokens || !bytes.Equal(prev.Options, next.Options):
		return "the settings changed"
	case !isPrefix(prev.Messages, next.Messages):
		return "an earlier message changed"
	}

	return ""
}

// isSummaryRequest reports whether a request asks for the summary a
// compaction replaces the conversation with.
func isSummaryRequest(params protocol.CompleteParams) bool {
	last := params.Messages[len(params.Messages)-1].Content

	return last[len(last)-1].Text == summarize
}

// Every request extends the one before it, through every path a session
// can take, so a provider can cache all of what came before. The one
// exception is the request after a compaction, which starts over from the
// summary.
func TestEveryRequestExtendsTheOneBefore(t *testing.T) {
	overloaded := protocol.Transient(errors.New("overloaded"))

	model := &scripted{
		failures: []error{overloaded},
		cut:      map[int]bool{1: true},
		answers: []protocol.Message{
			say(write("call-1", "a.txt", "one")),
			say(text("Working on it")),
			say(text("Done.")),
			say(write("call-2", "b.txt", "two")),
		},
	}

	worker, database, session, _ := setup(t, model)
	section := &changing{version: "one"}
	worker.Collectors = []ambient.Collector{section}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	worker.Inbox = &script{model: model, at: map[int]func(func(string) error) error{
		// The user says something while the first tool runs.
		2: func(record func(string) error) error { return record("name it a.md instead") },
		// The second run is cancelled while a request is in flight.
		5: func(func(string) error) error { cancel(); return nil },
	}}

	// A first task: a retried request, a tool, what the user said, and a
	// response the output limit cut.
	if err := worker.Run(context.Background(), session, "first task"); err != nil {
		t.Fatal(err)
	}

	// A second task, after the ambient context changed, cancelled mid-way.
	section.version = "two"

	if err := worker.Run(ctx, session, "second task"); err == nil {
		t.Fatal("the cancelled run did not fail")
	}

	// It is resumed, and its answer fills the context past the limit.
	model.answers = append(model.answers, say(text("Resumed and done.")))
	model.usage = protocol.Usage{Input: 100_000}
	worker.Compaction = Compaction{Tokens: 50_000}

	if err := worker.Resume(context.Background(), session); err != nil {
		t.Fatal(err)
	}

	// A third task starts by summarizing, and goes on from the summary.
	model.usage = protocol.Usage{}
	model.answers = append(model.answers,
		say(text("<summary>Two files written.</summary>")),
		say(write("call-3", "c.txt", "three")),
		say(text("Done.")),
	)

	if err := worker.Run(context.Background(), session, "third task"); err != nil {
		t.Fatal(err)
	}

	if len(model.requests) != 10 {
		t.Fatalf("%d requests, want 10: the scenario did not go where it should", len(model.requests))
	}

	for index := 1; index < len(model.requests); index++ {
		prev, next := model.requests[index-1], model.requests[index]

		if isSummaryRequest(prev) {
			continue
		}

		if why := extends(prev, next); why != "" {
			t.Errorf("request %d does not extend request %d: %s", index, index-1, why)
		}
	}

	// Every path the test means to cover was taken.
	entries, _ := database.Entries(session.ID)
	taken := kinds(entries)

	for _, kind := range []string{"steer", "harness", "context", "compaction"} {
		if !strings.Contains(taken, kind) {
			t.Errorf("the session has no %s entry: %s", kind, taken)
		}
	}

	if got := notes(t, database, session); got != "retry done cancelled done done" {
		t.Fatalf("notes = %q", got)
	}
}

// A response that read less from the provider's cache than the request
// before left there is told, and the session's history tells it again. A
// request to another model, or after a compaction, has nothing of the
// earlier ones to read, and misses nothing.
func TestACacheMissIsTold(t *testing.T) {
	model := &scripted{usages: map[int]protocol.Usage{
		0: {CacheWrite: 1000},
		1: {CacheRead: 1000, CacheWrite: 50},
		// Of the 1050 tokens the cache held, 750 were not read.
		2: {CacheRead: 300, CacheWrite: 800},
		3: {CacheWrite: 1200},
		// The summary is asked on the same start as the request before.
		4: {CacheRead: 1200, CacheWrite: 30},
		5: {CacheWrite: 400},
	}}

	worker, database, session, observer := setup(t, model)

	run := func(objective string) {
		t.Helper()

		model.answers = append(model.answers, say(text("ok")))

		if err := worker.Run(context.Background(), session, objective); err != nil {
			t.Fatal(err)
		}
	}

	run("first")
	run("second")
	run("third")

	worker.Model = "other"
	run("fourth")

	model.answers = append(model.answers, say(text("<summary>Four tasks.</summary>")))

	if _, err := worker.Compact(context.Background(), session); err != nil {
		t.Fatal(err)
	}

	run("fifth")

	want := []string{"cache miss: the provider read 300 of the 1050 tokens the previous request left in its cache; the other 750 counted as new input"}

	if !slices.Equal(observer.notices, want) {
		t.Fatalf("notices = %q, want %q", observer.notices, want)
	}

	entries, _ := database.Entries(session.ID)
	events, err := transcript.History(entries)

	if err != nil {
		t.Fatal(err)
	}

	var told []string

	for _, event := range events {
		if event.Type == protocol.EventLog && event.Text != transcript.Compacted {
			told = append(told, event.Text)
		}
	}

	if !slices.Equal(told, want) {
		t.Fatalf("the history told %q, want %q", told, want)
	}
}

// gives is an Inbox that tells the run files once the model has received
// a given number of requests.
type gives struct {
	model *scripted
	at    int
	files []string
}

func (g *gives) Take(record func(string, []string) error) error {
	if len(g.model.requests) != g.at || g.files == nil {
		return nil
	}

	files := g.files
	g.files = nil

	return record("", files)
}

// The files a person gives with an objective, or tells a run, are named to
// the model with it, and the objective's are named again after a
// compaction.
func TestFilesAreNamedToTheModelAndOutliveACompaction(t *testing.T) {
	model := &scripted{
		usage: protocol.Usage{Input: 120, Output: 10},
		answers: []protocol.Message{
			say(write("call-1", "a.txt", "one")),
			say(text("<summary>Started on the mockup.</summary>")),
			say(text("Done.")),
		},
	}

	worker, _, session, _ := setup(t, model)
	worker.Compaction = Compaction{Tokens: 100}
	worker.Inbox = &gives{model: model, at: 1, files: []string{"shots/crop.png"}}

	if err := worker.Run(context.Background(), session, "build this", "/tmp/mockup.pdf"); err != nil {
		t.Fatal(err)
	}

	first := model.requests[0].Messages[0].Content
	given := first[len(first)-1]

	if given.Text != "build this\n\n[file given: /tmp/mockup.pdf]" {
		t.Fatalf("the first request ends with %+v", given)
	}

	// The steer is recorded before the compaction, so the summary request
	// carries it; the request after starts from the summary alone.
	asked := model.requests[1].Messages
	steered := asked[len(asked)-1].Content

	if !slices.ContainsFunc(steered, func(block protocol.Block) bool {
		return block.Text == "[file given: shots/crop.png]"
	}) {
		t.Fatalf("the summary request does not name the steer's file: %+v", steered)
	}

	after := model.requests[2].Messages[0].Content
	summary := after[len(after)-1]

	if !strings.Contains(summary.Text, "build this\n\n[file given: /tmp/mockup.pdf]") {
		t.Fatalf("after the compaction the model saw %+v", summary)
	}
}
