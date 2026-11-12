package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"rumbo/internal/store"
	"rumbo/internal/transcript"
	"rumbo/protocol"
)

// hostFunction is one thing a front end can ask of the host itself, whatever
// the provider. A front end learns them from the host and offers them
// without knowing them beforehand, the way it does with a provider's.
type hostFunction struct {
	protocol.Function
	call func(h *host, ctx context.Context, params map[string]string) (protocol.HostResult, error)
}

// hostFunctions are listed to the front end in this order.
var hostFunctions = []hostFunction{
	{
		Function: protocol.Function{Name: "status", Description: "Show the session, the model, and what the conversation has cost"},
		call:     (*host).status,
	},
	{
		Function: protocol.Function{Name: "sessions", Description: "List the sessions of this project, most recently used first"},
		call:     (*host).listSessions,
	},
	{
		Function: protocol.Function{
			Name:        "open",
			Description: "Continue another session of this project",
			Params:      []protocol.Option{{Name: "session", Type: protocol.OptionString, Values: "sessions"}},
		},
		call: (*host).openSession,
	},
	{
		Function: protocol.Function{Name: "new", Description: "Start a new session with the next task"},
		call:     (*host).newSession,
	},
	{
		Function: protocol.Function{Name: "providers", Description: "List the registered providers"},
		call:     (*host).listProviders,
	},
	{
		Function: protocol.Function{
			Name:        "use",
			Description: "Change the provider and the model for the tasks that follow",
			Params: []protocol.Option{
				{Name: "provider", Type: protocol.OptionString, Values: "providers"},
				{Name: "model", Type: protocol.OptionString, Description: "A model of that provider"},
			},
		},
		call: (*host).useProvider,
	},
	{
		Function: protocol.Function{Name: "compact", Description: "Replace the conversation with a summary now, to free context"},
		call:     (*host).compactNow,
	},
	{
		Function: protocol.Function{Name: "quit", Description: "Stop the provider and exit"},
		call:     (*host).quit,
	},
}

// described returns the host functions as the front end is told about them.
func describedFunctions() []protocol.Function {
	functions := make([]protocol.Function, len(hostFunctions))

	for index, function := range hostFunctions {
		functions[index] = function.Function
	}

	return functions
}

// invokeHost runs a function of the host. Every answer carries the state
// the front end is attached to afterwards, since the call may change it.
func (h *host) invokeHost(ctx context.Context, name string, raw json.RawMessage) (protocol.HostResult, error) {
	params := map[string]string{}

	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return protocol.HostResult{}, fmt.Errorf("the parameters of %s are an object of text values: %w", name, err)
		}
	}

	for _, function := range hostFunctions {
		if function.Name != name {
			continue
		}

		for _, param := range function.Params {
			if params[param.Name] == "" {
				return protocol.HostResult{}, fmt.Errorf("%s needs %s", name, param.Name)
			}
		}

		result, err := function.call(h, ctx, params)
		result.State = h.state()

		return result, err
	}

	return protocol.HostResult{}, fmt.Errorf("the host has no function %q", name)
}

func (h *host) state() protocol.HostState {
	return protocol.HostState{
		Dir:      h.info.Dir,
		Session:  h.info.Session,
		Provider: h.settings.Provider,
		Model:    h.settings.Model,
	}
}

func (h *host) status(context.Context, map[string]string) (protocol.HostResult, error) {
	var out strings.Builder

	fmt.Fprintf(&out, "provider: %s\nmodel: %s\ndirectory: %s", h.settings.Provider, h.settings.Model, h.info.Dir)

	if len(h.settings.Options) > 0 {
		fmt.Fprintf(&out, "\noptions: %s", h.settings.Options)
	}

	if h.session == nil {
		out.WriteString("\nsession: none yet; the next task starts one")

		return protocol.HostResult{Text: out.String()}, nil
	}

	entries, err := h.database.Entries(h.session.ID)

	if err != nil {
		return protocol.HostResult{}, err
	}

	state, err := transcript.Build(entries)

	if err != nil {
		return protocol.HostResult{}, err
	}

	var spent protocol.Usage
	turns := 0

	for _, entry := range entries {
		var answer struct {
			Usage protocol.Usage `json:"usage"`
		}

		if entry.Kind != transcript.KindAssistant && entry.Kind != transcript.KindCompaction {
			continue
		}

		if json.Unmarshal(entry.Payload, &answer) != nil {
			continue
		}

		turns++
		spent.Input += answer.Usage.Input + answer.Usage.CacheWrite
		spent.CacheRead += answer.Usage.CacheRead
		spent.Output += answer.Usage.Output
	}

	fmt.Fprintf(&out, "\nsession: %s (%s)", h.session.ID, h.session.Title)
	fmt.Fprintf(&out, "\ncontext: %d tokens", state.Tokens)
	fmt.Fprintf(&out, "\nspent: %d requests, %d input tokens, %d read from cache, %d output", turns, spent.Input, spent.CacheRead, spent.Output)

	if state.Unfinished {
		out.WriteString("\nthe last run did not finish, and can be resumed")
	}

	return protocol.HostResult{Text: out.String()}, nil
}

func (h *host) listSessions(context.Context, map[string]string) (protocol.HostResult, error) {
	sessions, err := h.database.Sessions(h.root)

	if err != nil {
		return protocol.HostResult{}, err
	}

	var result protocol.HostResult

	for _, session := range sessions {
		result.Values = append(result.Values, protocol.Value{
			Value:       session.ID,
			Label:       session.Title,
			Description: "last used " + session.ActiveAt.Format("2006-01-02 15:04"),
		})
	}

	if len(sessions) == 0 {
		result.Text = "this project has no sessions yet"
	}

	return result, nil
}

func (h *host) openSession(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	found, err := h.database.Session(params["session"])

	if err != nil {
		return protocol.HostResult{}, fmt.Errorf("session %s: %w", params["session"], err)
	}

	// Settings were read for this project, so the session has to be one of
	// its own.
	if found.Project != h.root {
		return protocol.HostResult{}, fmt.Errorf("session %s belongs to %s; start rumbo there to continue it", found.ID, found.Project)
	}

	unlock, err := h.database.LockSession(found.ID)

	if err != nil && (h.session == nil || h.session.ID != found.ID) {
		return protocol.HostResult{}, fmt.Errorf("session %s: %w", found.ID, err)
	}

	if err == nil {
		h.release()
		h.unlock = unlock
	}

	h.session = &found
	h.info.Session = found.ID
	h.info.Dir = found.Dir

	return protocol.HostResult{Text: fmt.Sprintf("continuing session %s (%s) in %s", found.ID, found.Title, found.Dir)}, nil
}

func (h *host) newSession(context.Context, map[string]string) (protocol.HostResult, error) {
	h.release()
	h.session = nil
	h.info.Session = ""

	return protocol.HostResult{Text: "the next task starts a new session"}, nil
}

func (h *host) listProviders(context.Context, map[string]string) (protocol.HostResult, error) {
	programs, err := h.database.Programs(store.KindProvider)

	if err != nil {
		return protocol.HostResult{}, err
	}

	var result protocol.HostResult

	for _, program := range programs {
		result.Values = append(result.Values, protocol.Value{Value: program.Name, Description: strings.Join(program.Command, " ")})
	}

	return result, nil
}

// useProvider changes the provider and the model. The running provider is
// stopped and the new one starts with the next request. The session goes on:
// the history is the host's, not the provider's.
func (h *host) useProvider(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	name := params["provider"]

	if _, err := h.database.Program(store.KindProvider, name); err != nil {
		return protocol.HostResult{}, fmt.Errorf("provider %q is not registered", name)
	}

	options, err := storedOptions(h.database, h.root, name)

	if err != nil {
		return protocol.HostResult{}, err
	}

	if h.process != nil {
		_ = h.process.Close()
	}

	h.process = nil
	h.worker = nil

	// Options given on the command line were meant for the provider rumbo
	// was started with, so only the stored ones carry over.
	h.settings.Provider = name
	h.settings.Model = params["model"]
	h.settings.Options = options
	h.info.Provider = name
	h.info.Model = params["model"]

	return protocol.HostResult{Text: fmt.Sprintf("using %s/%s", name, params["model"])}, nil
}

func (h *host) compactNow(ctx context.Context, _ map[string]string) (protocol.HostResult, error) {
	if h.session == nil {
		return protocol.HostResult{}, errors.New("there is no session to compact yet")
	}

	if err := h.start(); err != nil {
		return protocol.HostResult{}, err
	}

	if err := h.lock(); err != nil {
		return protocol.HostResult{}, err
	}

	h.worker.Model = h.settings.Model
	h.worker.Options = h.settings.Options

	before, err := h.worker.Compact(ctx, *h.session)

	if err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: fmt.Sprintf("the conversation, %d tokens, was replaced by a summary", before)}, nil
}

func (h *host) quit(context.Context, map[string]string) (protocol.HostResult, error) {
	h.conn.Stop()

	return protocol.HostResult{Text: "bye"}, nil
}
