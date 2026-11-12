// Package agent runs the loop that drives a model to finish an objective:
// record the request, ask the provider, execute the tool calls, and verify
// the work before accepting that it is done.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"rumbo/internal/ambient"
	"rumbo/internal/provider"
	"rumbo/internal/store"
	"rumbo/internal/tool"
	"rumbo/internal/transcript"
	"rumbo/internal/verify"
	"rumbo/protocol"
)

// Observer is told what happens during a run. It is how a front end renders
// progress; the agent works the same whoever listens.
type Observer interface {
	ContextUpdated(sections []string)
	Text(delta string)
	ToolStarted(name string, args json.RawMessage)
	ToolFinished(name, result string, failed bool)
	Verified(checks []string, result verify.Result)
	TurnFinished(usage protocol.Usage)
}

// Agent holds everything a run needs. Every dependency is an interface or
// plain data, so each one can be replaced independently.
type Agent struct {
	Store      store.Store
	Provider   provider.Provider
	Observer   Observer
	Tools      []tool.Tool
	Collectors []ambient.Collector

	// ProviderName and Model are recorded with every response.
	ProviderName string
	Model        string
	MaxTokens    int

	// Checks run when the agent stops after changing the project. The agent
	// is sent back to work at most MaxVerifyRounds times per run.
	Checks          []string
	MaxVerifyRounds int
}

// Run works on objective in the given session until the model stops and the
// checks pass, the verify rounds run out, or ctx is cancelled.
func (a *Agent) Run(ctx context.Context, session store.Session, objective string) error {
	if err := a.open(ctx, session, objective); err != nil {
		return err
	}

	changed := false
	rounds := 0

	for {
		calls, err := a.turn(ctx, session)

		if err != nil {
			return err
		}

		if len(calls) > 0 {
			mutated, err := a.execute(ctx, session, calls)
			changed = changed || mutated

			if err != nil {
				return err
			}

			continue
		}

		if !changed || len(a.Checks) == 0 {
			return nil
		}

		result := verify.Run(ctx, session.Dir, a.Checks)

		if ctx.Err() != nil {
			return ctx.Err()
		}

		a.Observer.Verified(a.Checks, result)

		if result.Passed {
			return nil
		}

		if rounds >= a.MaxVerifyRounds {
			return fmt.Errorf("the checks still fail after %d attempt(s) to fix them", rounds)
		}

		rounds++
		changed = false

		if err := a.append(session, transcript.KindHarness, transcript.Text{Text: feedback(objective, result)}); err != nil {
			return err
		}
	}
}

// open records what the model needs before the first request of a run: the
// system prompt if it changed, the ambient context that changed, and the
// objective.
func (a *Agent) open(ctx context.Context, session store.Session, objective string) error {
	entries, err := a.Store.Entries(session.ID)

	if err != nil {
		return err
	}

	state, err := transcript.Build(entries)

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

	if changes := ambient.Changes(ctx, session.Dir, a.Collectors, state.Sections); len(changes) > 0 {
		if err := a.append(session, transcript.KindContext, transcript.Context{Sections: changes}); err != nil {
			return err
		}

		names := make([]string, len(changes))

		for index, section := range changes {
			names[index] = section.Name
		}

		a.Observer.ContextUpdated(names)
	}

	return a.append(session, transcript.KindUser, transcript.Text{Text: objective})
}

// turn sends the session to the provider, records the answer, and returns the
// tool calls it contains.
func (a *Agent) turn(ctx context.Context, session store.Session) ([]protocol.Block, error) {
	entries, err := a.Store.Entries(session.ID)

	if err != nil {
		return nil, err
	}

	state, err := transcript.Build(entries)

	if err != nil {
		return nil, err
	}

	result, err := a.Provider.Complete(ctx, protocol.CompleteParams{
		Model:     a.Model,
		System:    state.System.Text,
		Messages:  state.Messages,
		Tools:     state.System.Tools,
		MaxTokens: a.MaxTokens,
	}, a.Observer.Text)

	if err != nil {
		return nil, err
	}

	result.Message.Role = protocol.RoleAssistant

	err = a.append(session, transcript.KindAssistant, transcript.Assistant{
		Message:  result.Message,
		Stop:     result.Stop,
		Usage:    result.Usage,
		Provider: a.ProviderName,
		Model:    a.Model,
	})

	if err != nil {
		return nil, err
	}

	a.Observer.TurnFinished(result.Usage)

	switch result.Stop {
	case protocol.StopRefusal:
		return nil, fmt.Errorf("the model refused the request")
	case protocol.StopMaxTokens:
		return nil, fmt.Errorf("the response hit the output token limit; raise the max_tokens setting or split the task")
	}

	var calls []protocol.Block

	for _, block := range result.Message.Content {
		if block.Type == protocol.BlockToolCall {
			calls = append(calls, block)
		}
	}

	return calls, nil
}

// execute runs the calls in order, records each result, and reports whether
// any of them may have changed the project. Calls left without a result by a
// cancel are closed by the transcript on the next request.
func (a *Agent) execute(ctx context.Context, session store.Session, calls []protocol.Block) (bool, error) {
	changed := false

	for _, call := range calls {
		if ctx.Err() != nil {
			return changed, ctx.Err()
		}

		a.Observer.ToolStarted(call.Name, call.Args)

		text, failed, mutates := a.call(ctx, session.Dir, call)
		changed = changed || mutates

		a.Observer.ToolFinished(call.Name, text, failed)

		result := transcript.ToolResult{ID: call.ID, Name: call.Name, Text: text, IsError: failed}

		if err := a.append(session, transcript.KindToolResult, result); err != nil {
			return changed, err
		}
	}

	return changed, ctx.Err()
}

func (a *Agent) call(ctx context.Context, dir string, call protocol.Block) (text string, failed, mutates bool) {
	for _, candidate := range a.Tools {
		if candidate.Def().Name != call.Name {
			continue
		}

		// Arguments arrive as the model generated them, so they are checked
		// here, at the boundary, before a tool sees them.
		if !json.Valid(call.Args) {
			return "invalid arguments: not valid JSON", true, false
		}

		output, err := candidate.Run(ctx, dir, call.Args)

		if err != nil {
			return err.Error(), true, candidate.Mutates()
		}

		return output, false, candidate.Mutates()
	}

	return fmt.Sprintf("unknown tool %q", call.Name), true, false
}

func (a *Agent) append(session store.Session, kind string, payload any) error {
	_, err := a.Store.Append(session.ID, kind, payload)

	return err
}

func (a *Agent) toolDefs() []protocol.ToolDef {
	defs := make([]protocol.ToolDef, len(a.Tools))

	for index, candidate := range a.Tools {
		defs[index] = candidate.Def()
	}

	return defs
}

// feedback is what the model is told when the checks fail. It restates the
// objective so a long run is pulled back to what the user asked for.
func feedback(objective string, result verify.Result) string {
	return fmt.Sprintf(
		"[harness] The project's checks fail after your changes. Fix them, then finish.\n\n%s\n\nObjective, for reference:\n%s",
		result.Report, objective,
	)
}
