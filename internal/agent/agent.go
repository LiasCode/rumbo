// Package agent runs the loop that drives a model to finish an objective:
// record the request, ask the provider, execute the tool calls, and repeat
// until the model stops.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"rumbo/internal/ambient"
	"rumbo/internal/provider"
	"rumbo/internal/store"
	"rumbo/internal/tool"
	"rumbo/internal/transcript"
	"rumbo/protocol"
)

// Observer is told what happens during a run. It is how a front end renders
// progress; the agent works the same whoever listens.
type Observer interface {
	ContextUpdated(sections []string)
	Text(delta string)
	ToolStarted(name string, args json.RawMessage)
	ToolFinished(name, result string, failed bool)
	TurnFinished(usage protocol.Usage)
	// Notice reports something the loop did on its own, such as a retry.
	Notice(text string)
}

// DefaultRetryWaits are the pauses before each retry of a request that
// failed for a transient reason. After the last one the run fails.
var DefaultRetryWaits = []time.Duration{2 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute}

// maxContinuations is how many responses in a row may be cut by the output
// limit before the run gives up instead of looping.
const maxContinuations = 3

// Messages from the harness to the model. They are entries like any other,
// so the model's history shows exactly what it was told.
const (
	cutText = "[harness] Your response was cut off by the output limit. Continue exactly where it stopped."

	cutCall = "not executed: the response was cut off by the output limit before this call was complete. Send it again; if it is large, split the work into smaller calls."

	resumed = "[harness] The run was interrupted before it finished, and tool calls in progress did not complete. Check the current state of the project and continue working on the objective."

	// summarize asks for the summary that will replace the conversation. The
	// last sentence matters: the request still declares the tools, because
	// dropping them would change the cached prefix.
	summarize = "[harness] The context is almost full. Summarize this conversation inside <summary></summary> tags, so that the work continues from the summary alone, without redoing anything and without being told again what matters. Preserve: (1) the objective, in the user's own words, and every constraint, preference, or decision the user stated; (2) what has been done so far: which files were created or changed and how, and which commands were run with what result; (3) problems that came up and how they were handled; (4) approaches that were tried or ruled out, and why; (5) exactly where the work stands now and what is left to do, in order; (6) details that are hard to reconstruct: paths, names, numbers, exact error messages. Be complete on these even at the cost of length, and brief on everything else. Do not call any tools while writing this summary; respond with text only."

	compacted = "[harness] The conversation before this point was replaced by this summary to free context:\n\n<summary>\n%s\n</summary>\n\nThe objective is still:\n%s\n\nContinue from where the summary ends. The files are as the summary describes; read them again when you need their content."
)

// Agent holds everything a run needs. Every dependency is an interface or
// plain data, so each one can be replaced independently.
type Agent struct {
	Store      store.Store
	Provider   provider.Provider
	Observer   Observer
	Tools      []tool.Tool
	Collectors []ambient.Collector

	// ProviderName, Model, and Options are recorded with every response.
	// Options are the provider's own; the agent only carries them.
	ProviderName string
	Model        string
	Options      json.RawMessage
	MaxTokens    int

	// CompactAt is the context size, in tokens, at which the conversation is
	// replaced by a summary. Zero never compacts.
	CompactAt int64
	// MaxTurns ends a run that keeps going. Zero means no limit.
	MaxTurns int
	// RetryWaits are the pauses before retrying a transient failure.
	RetryWaits []time.Duration
}

// Run works on objective in the given session until the model stops, a
// limit is reached, or ctx is cancelled.
func (a *Agent) Run(ctx context.Context, session store.Session, objective string) error {
	err := a.prepare(ctx, session)

	if err == nil {
		err = a.append(session, transcript.KindUser, transcript.Text{Text: objective})
	}

	if err == nil {
		err = a.loop(ctx, session, objective)
	}

	return a.finish(ctx, session, err)
}

// Resume continues the session's last run, which ended by failure, cancel,
// or the death of the process, without a new objective.
func (a *Agent) Resume(ctx context.Context, session store.Session) error {
	state, err := a.state(session)

	if err != nil {
		return err
	}

	if !state.Unfinished {
		return errors.New("nothing to resume: the last run of this session finished")
	}

	err = a.prepare(ctx, session)

	if err == nil {
		err = a.append(session, transcript.KindHarness, transcript.Text{Text: resumed})
	}

	if err == nil {
		err = a.loop(ctx, session, state.Objective)
	}

	return a.finish(ctx, session, err)
}

// loop asks the model and runs its tool calls until it stops calling them,
// which is how the model says the work is done.
func (a *Agent) loop(ctx context.Context, session store.Session, objective string) error {
	cuts := 0

	for turns := 1; ; turns++ {
		if a.MaxTurns > 0 && turns > a.MaxTurns {
			return fmt.Errorf("the run was stopped after %d turns (the max_turns setting); resume it to continue", a.MaxTurns)
		}

		if err := a.compact(ctx, session, objective); err != nil {
			return err
		}

		reply, err := a.turn(ctx, session)

		if err != nil {
			return err
		}

		calls := toolCalls(reply.Message)

		switch reply.Stop {
		case protocol.StopRefusal:
			return errors.New("the model refused the request")

		case protocol.StopMaxTokens:
			if cuts++; cuts > maxContinuations {
				return fmt.Errorf("the response hit the output limit %d times in a row; raise the max_tokens setting", cuts)
			}

			if err := a.continueCut(session, calls); err != nil {
				return err
			}

			continue
		}

		cuts = 0

		if len(calls) > 0 {
			if err := a.execute(ctx, session, calls); err != nil {
				return err
			}

			continue
		}

		return nil
	}
}

// finish records how the run ended, so the session shows it and a later
// resume knows there is something to continue.
func (a *Agent) finish(ctx context.Context, session store.Session, err error) error {
	switch {
	case err == nil:
		return a.note(session, transcript.NoteDone, "")
	case ctx.Err() != nil:
		_ = a.note(session, transcript.NoteCancelled, "")
	default:
		_ = a.note(session, transcript.NoteFailed, err.Error())
	}

	return err
}

// prepare records what the model needs before a run continues: the system
// prompt if it changed, and the ambient context that changed.
func (a *Agent) prepare(ctx context.Context, session store.Session) error {
	state, err := a.state(session)

	if err != nil {
		return err
	}

	system := transcript.System{Text: SystemPrompt, Tools: a.toolDefs()}

	// Compared as stored, because that is the form the recorded one has.
	recorded, _ := json.Marshal(state.System)
	current, _ := json.Marshal(system)

	if !bytes.Equal(recorded, current) {
		if err := a.append(session, transcript.KindSystem, system); err != nil {
			return err
		}
	}

	changes := ambient.Changes(ctx, session.Dir, a.Collectors, state.Sections)

	if len(changes) == 0 {
		return nil
	}

	names := make([]string, len(changes))

	for index, section := range changes {
		names[index] = section.Name
	}

	a.Observer.ContextUpdated(names)

	return a.append(session, transcript.KindContext, transcript.Context{Sections: changes})
}

// Compact replaces the session's conversation with a summary now, whatever
// its size, and returns how large the context was. It is what a user asks
// for before a long task, or to stop paying for a history that is spent.
func (a *Agent) Compact(ctx context.Context, session store.Session) (int64, error) {
	state, err := a.state(session)

	if err != nil {
		return 0, err
	}

	if len(state.Messages) < 2 {
		return 0, errors.New("there is no conversation to compact yet")
	}

	return state.Tokens, a.summarize(ctx, session, state, state.Objective)
}

// compact replaces the conversation with a summary once the context has
// grown to CompactAt.
func (a *Agent) compact(ctx context.Context, session store.Session, objective string) error {
	state, err := a.state(session)

	if err != nil || a.CompactAt == 0 || state.Tokens < a.CompactAt {
		return err
	}

	a.Observer.Notice(fmt.Sprintf("the context reached %d tokens; summarizing the conversation to free it", state.Tokens))

	return a.summarize(ctx, session, state, objective)
}

// summarize asks the model for a summary of the conversation, as one more
// request on the same prefix, and records the compaction the next request
// starts from.
func (a *Agent) summarize(ctx context.Context, session store.Session, state transcript.State, objective string) error {

	// The instruction joins the last user message when there is one, since
	// roles must alternate.
	messages := slices.Clone(state.Messages)
	instruction := protocol.Block{Type: protocol.BlockText, Text: summarize}
	last := len(messages) - 1

	if last >= 0 && messages[last].Role == protocol.RoleUser {
		messages[last].Content = append(slices.Clone(messages[last].Content), instruction)
	} else {
		messages = append(messages, protocol.Message{Role: protocol.RoleUser, Content: []protocol.Block{instruction}})
	}

	// The summary is not an answer to the user, so it is not shown as text.
	result, err := a.complete(ctx, session, a.params(state, messages), func(string) {})

	if err != nil {
		return fmt.Errorf("could not summarize the conversation: %w", err)
	}

	a.Observer.TurnFinished(result.Usage)

	summary := summaryOf(result)

	if summary == "" {
		return errors.New("could not summarize the conversation: the model did not return a summary; raise the compact_at setting or start a new session")
	}

	return a.append(session, transcript.KindCompaction, transcript.Compaction{
		Prompt:   summarize,
		Text:     fmt.Sprintf(compacted, summary, objective),
		Usage:    result.Usage,
		Provider: a.ProviderName,
		Model:    a.Model,
		Options:  a.Options,
	})
}

// summaryOf extracts the summary from a response to the summarize
// instruction. A response that was cut short or that calls tools has none.
func summaryOf(result protocol.CompleteResult) string {
	if result.Stop != protocol.StopEnd {
		return ""
	}

	var text strings.Builder

	for _, block := range result.Message.Content {
		if block.Type == protocol.BlockText {
			text.WriteString(block.Text)
		}
	}

	summary := text.String()

	if _, after, found := strings.Cut(summary, "<summary>"); found {
		summary, _, _ = strings.Cut(after, "</summary>")
	}

	return strings.TrimSpace(summary)
}

func (a *Agent) params(state transcript.State, messages []protocol.Message) protocol.CompleteParams {
	return protocol.CompleteParams{
		Model:     a.Model,
		System:    state.System.Text,
		Messages:  messages,
		Tools:     state.System.Tools,
		MaxTokens: a.MaxTokens,
		Options:   a.Options,
	}
}

// complete sends one request. A request that fails for a transient reason is
// sent again after a pause.
func (a *Agent) complete(ctx context.Context, session store.Session, params protocol.CompleteParams, onDelta func(string)) (protocol.CompleteResult, error) {
	for attempt := 0; ; attempt++ {
		result, err := a.Provider.Complete(ctx, params, onDelta)

		if err == nil {
			return result, nil
		}

		if ctx.Err() != nil || !protocol.IsTransient(err) || attempt >= len(a.RetryWaits) {
			return result, err
		}

		wait := a.RetryWaits[attempt]
		notice := fmt.Sprintf("request failed (%s); retrying in %s", err, wait)

		a.Observer.Notice(notice)

		if err := a.note(session, transcript.NoteRetry, notice); err != nil {
			return result, err
		}

		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// turn sends the session to the provider and records the answer.
func (a *Agent) turn(ctx context.Context, session store.Session) (protocol.CompleteResult, error) {
	state, err := a.state(session)

	if err != nil {
		return protocol.CompleteResult{}, err
	}

	streamed := false

	onDelta := func(text string) {
		streamed = true
		a.Observer.Text(text)
	}

	result, err := a.complete(ctx, session, a.params(state, state.Messages), onDelta)

	if err != nil {
		return result, err
	}

	result.Message.Role = protocol.RoleAssistant

	// Deltas are optional for a provider. Without them the text is shown
	// when the message arrives, so the user sees the answer either way.
	if !streamed {
		for _, block := range result.Message.Content {
			if block.Type == protocol.BlockText {
				a.Observer.Text(block.Text)
			}
		}
	}

	err = a.append(session, transcript.KindAssistant, transcript.Assistant{
		Message:  result.Message,
		Stop:     result.Stop,
		Usage:    result.Usage,
		Provider: a.ProviderName,
		Model:    a.Model,
		Options:  a.Options,
	})

	a.Observer.TurnFinished(result.Usage)

	return result, err
}

// continueCut answers a response that the output limit cut short, so the
// model goes on instead of the run ending. Its tool calls are not executed:
// any of them may be incomplete.
func (a *Agent) continueCut(session store.Session, calls []protocol.Block) error {
	a.Observer.Notice("the response was cut off by the output limit; asking the model to continue")

	if len(calls) == 0 {
		return a.append(session, transcript.KindHarness, transcript.Text{Text: cutText})
	}

	for _, call := range calls {
		result := transcript.ToolResult{ID: call.ID, Name: call.Name, Text: cutCall, IsError: true}

		if err := a.append(session, transcript.KindToolResult, result); err != nil {
			return err
		}
	}

	return nil
}

// execute runs the calls in order and records each result. Calls left
// without a result by a cancel are closed by the transcript on the next
// request.
func (a *Agent) execute(ctx context.Context, session store.Session, calls []protocol.Block) error {
	for _, call := range calls {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		a.Observer.ToolStarted(call.Name, call.Args)

		text, failed := a.call(ctx, session.Dir, call)

		a.Observer.ToolFinished(call.Name, text, failed)

		result := transcript.ToolResult{ID: call.ID, Name: call.Name, Text: text, IsError: failed}

		if err := a.append(session, transcript.KindToolResult, result); err != nil {
			return err
		}
	}

	return ctx.Err()
}

func (a *Agent) call(ctx context.Context, dir string, call protocol.Block) (text string, failed bool) {
	for _, candidate := range a.Tools {
		if candidate.Def().Name != call.Name {
			continue
		}

		// Arguments arrive as the model generated them, so they are checked
		// here, at the boundary, before a tool sees them.
		if !json.Valid(call.Args) {
			return "invalid arguments: not valid JSON", true
		}

		output, err := candidate.Run(ctx, dir, call.Args)

		if err != nil {
			return err.Error(), true
		}

		return output, false
	}

	return fmt.Sprintf("unknown tool %q", call.Name), true
}

func (a *Agent) state(session store.Session) (transcript.State, error) {
	entries, err := a.Store.Entries(session.ID)

	if err != nil {
		return transcript.State{}, err
	}

	return transcript.Build(entries)
}

func (a *Agent) append(session store.Session, kind string, payload any) error {
	_, err := a.Store.Append(session.ID, kind, payload)

	return err
}

func (a *Agent) note(session store.Session, kind, text string) error {
	return a.append(session, transcript.KindNote, transcript.Note{Type: kind, Text: text})
}

func (a *Agent) toolDefs() []protocol.ToolDef {
	defs := make([]protocol.ToolDef, len(a.Tools))

	for index, candidate := range a.Tools {
		defs[index] = candidate.Def()
	}

	return defs
}

func toolCalls(message protocol.Message) []protocol.Block {
	var calls []protocol.Block

	for _, block := range message.Content {
		if block.Type == protocol.BlockToolCall {
			calls = append(calls, block)
		}
	}

	return calls
}
