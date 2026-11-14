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
	// duringRun says the function also answers while a run works. It only
	// reads, or stores what applies from the next start, and answers at
	// once. The others change the session or the provider, or need a
	// provider, and wait for the run to end.
	duringRun bool
	call      func(h *host, ctx context.Context, params map[string]string) (protocol.HostResult, error)
}

// hostFunctions are listed to the front end in this order.
var hostFunctions = []hostFunction{
	{
		Function:  protocol.Function{Name: "status", Description: "Show the session, the model, and what the conversation has cost"},
		duringRun: true,
		call:      (*host).status,
	},
	{
		Function:  protocol.Function{Name: "sessions", Description: "List the sessions of this project, most recently used first"},
		duringRun: true,
		call:      (*host).listSessions,
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
		Function: protocol.Function{
			Name:        "delete",
			Description: "Delete a session of this project and its history",
			Params:      []protocol.Option{{Name: "session", Type: protocol.OptionString, Values: "sessions"}},
			Confirm:     "This deletes the session and everything recorded in it, for good. Files it changed stay as they are.",
		},
		call: (*host).deleteSession,
	},
	{
		Function:  protocol.Function{Name: "providers", Description: "List the registered providers"},
		duringRun: true,
		call:      (*host).listProviders,
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
		Function: protocol.Function{
			Name:        "register",
			Description: "Register a provider: a name and the command that starts it",
			Params: []protocol.Option{
				{Name: "name", Type: protocol.OptionString},
				{Name: "command", Type: protocol.OptionString, Description: "The program and its arguments, separated by spaces"},
			},
		},
		duringRun: true,
		call:      (*host).registerProvider,
	},
	{
		Function: protocol.Function{
			Name:        "configure",
			Description: "Change a field of a provider's configuration; an empty value removes it",
			Params: []protocol.Option{
				{Name: "provider", Type: protocol.OptionString, Values: "providers"},
				{Name: "change", Type: protocol.OptionString, Description: "field=value"},
			},
		},
		call: (*host).configureProvider,
	},
	{
		Function: protocol.Function{
			Name:        "inspect",
			Description: "Start a provider and show what it offers; also the test that it works",
			Params:      []protocol.Option{{Name: "provider", Type: protocol.OptionString, Values: "providers"}},
		},
		call: (*host).inspect,
	},
	{
		Function: protocol.Function{
			Name:        "unregister",
			Description: "Remove a registered provider",
			Params:      []protocol.Option{{Name: "provider", Type: protocol.OptionString, Values: "providers"}},
			Confirm:     "This removes the provider's registration and its configuration. The program itself is not touched.",
		},
		duringRun: true,
		call:      (*host).unregisterProvider,
	},
	{
		Function:  protocol.Function{Name: "settings", Description: "Show every setting as it applies here, and where it comes from"},
		duringRun: true,
		call:      (*host).showSettings,
	},
	{
		Function: protocol.Function{
			Name:        "setting",
			Description: "Store a setting, for this project unless the scope says global",
			Params: []protocol.Option{
				{Name: "key", Type: protocol.OptionString, Values: "setting-keys"},
				{Name: "value", Type: protocol.OptionString},
				{Name: "scope", Type: protocol.OptionString, Enum: scopes, Optional: true},
			},
		},
		duringRun: true,
		call:      (*host).storeSetting,
	},
	{
		Function: protocol.Function{
			Name:        "unset",
			Description: "Remove a stored setting, for this project unless the scope says global",
			Params: []protocol.Option{
				{Name: "key", Type: protocol.OptionString, Values: "setting-keys"},
				{Name: "scope", Type: protocol.OptionString, Enum: scopes, Optional: true},
			},
		},
		duringRun: true,
		call:      (*host).removeSetting,
	},
	{
		Function:  protocol.Function{Name: "setting-keys", Description: "List the settings there are"},
		duringRun: true,
		call:      (*host).settingKeys,
	},
	{
		Function: protocol.Function{Name: "compact", Description: "Replace the conversation with a summary now, to free context"},
		call:     (*host).compactNow,
	},
	{
		Function:  protocol.Function{Name: "quit", Description: "Stop the provider and exit; a run in progress is cancelled"},
		duringRun: true,
		call:      (*host).quit,
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
// running says a run is in progress, which most functions wait for.
func (h *host) invokeHost(ctx context.Context, name string, raw json.RawMessage, running bool) (protocol.HostResult, error) {
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

		if running && !function.duringRun {
			return protocol.HostResult{}, fmt.Errorf("%s waits for the run in progress to end; cancel the run to use it now", name)
		}

		for _, param := range function.Params {
			if params[param.Name] == "" && !param.Optional {
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
	h.mu.Lock()
	defer h.mu.Unlock()

	return protocol.HostState{
		Dir:      h.info.Dir,
		Session:  h.info.Session,
		Provider: h.settings.Provider,
		Model:    h.settings.Model,
	}
}

func (h *host) status(context.Context, map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	session, dir, working := h.session, h.info.Dir, h.working
	h.mu.Unlock()

	var out strings.Builder

	fmt.Fprintf(&out, "provider: %s\nmodel: %s\ndirectory: %s", h.settings.Provider, h.settings.Model, dir)

	if len(h.settings.Options) > 0 {
		fmt.Fprintf(&out, "\noptions: %s", h.settings.Options)
	}

	if session == nil {
		out.WriteString("\nsession: none yet; the next task starts one")

		return protocol.HostResult{Text: out.String()}, nil
	}

	entries, err := h.database.Entries(session.ID)

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

	fmt.Fprintf(&out, "\nsession: %s (%s)", session.ID, session.Title)
	fmt.Fprintf(&out, "\ncontext: %d tokens", state.Tokens)

	if state.Window > 0 {
		fmt.Fprintf(&out, " of %d, %d%%", state.Window, state.Tokens*100/state.Window)
	}

	if limit := h.settings.Compaction.Limit(state.Window); limit > 0 {
		fmt.Fprintf(&out, "; summarized at %d", limit)
	}
	fmt.Fprintf(&out, "\nspent: %d requests, %d input tokens, %d read from cache, %d output", turns, spent.Input, spent.CacheRead, spent.Output)

	switch {
	case working:
		out.WriteString("\na run is in progress")
	case state.Unfinished:
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

// deleteSession removes a session for good. One that another rumbo process
// is using is left alone.
func (h *host) deleteSession(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	found, err := h.database.Session(params["session"])

	if err != nil {
		return protocol.HostResult{}, fmt.Errorf("session %s: %w", params["session"], err)
	}

	if found.Project != h.root {
		return protocol.HostResult{}, fmt.Errorf("session %s belongs to %s; start rumbo there to delete it", found.ID, found.Project)
	}

	// The session in use here is let go of first; any other must be free.
	if h.session != nil && h.session.ID == found.ID {
		h.release()
		h.session = nil
		h.info.Session = ""
	} else {
		unlock, err := h.database.LockSession(found.ID)

		if err != nil {
			return protocol.HostResult{}, fmt.Errorf("session %s: %w", found.ID, err)
		}

		defer unlock()
	}

	if err := h.database.DeleteSession(found.ID); err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: fmt.Sprintf("deleted session %s (%s)", found.ID, found.Title)}, nil
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

// scopes are where a setting can be stored from a front end.
var scopes = []string{sourceProject, sourceGlobal}

// scope turns the scope a function was given into the one the store uses.
// A front end works in a project, so that is where a setting goes unless it
// says otherwise.
func (h *host) scope(given string) (string, error) {
	switch given {
	case "", sourceProject:
		return h.root, nil
	case sourceGlobal:
		return store.GlobalScope, nil
	}

	return "", fmt.Errorf("the scope is %s", strings.Join(scopes, " or "))
}

func (h *host) showSettings(context.Context, map[string]string) (protocol.HostResult, error) {
	settings, err := effectiveSettings(h.database, h.root)

	if err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: describeSettings(settings)}, nil
}

func (h *host) settingKeys(context.Context, map[string]string) (protocol.HostResult, error) {
	var result protocol.HostResult

	for _, spec := range settingSpecs {
		result.Values = append(result.Values, protocol.Value{Value: spec.key, Description: spec.help})
	}

	result.Values = append(result.Values, protocol.Value{
		Value:       optionsPrefix + h.settings.Provider,
		Description: "JSON object of option values sent to this provider with every request",
	})

	return result, nil
}

// storeSetting stores a setting. The host read its settings when it started,
// so what is stored applies from the next start.
func (h *host) storeSetting(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	scope, err := h.scope(params["scope"])

	if err != nil {
		return protocol.HostResult{}, err
	}

	if err := setSetting(h.database, scope, params["key"], params["value"]); err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: fmt.Sprintf("%s = %s, stored; it applies from the next start of rumbo", params["key"], params["value"])}, nil
}

func (h *host) removeSetting(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	scope, err := h.scope(params["scope"])

	if err != nil {
		return protocol.HostResult{}, err
	}

	if err := unsetSetting(h.database, scope, params["key"]); err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: params["key"] + " is no longer stored there"}, nil
}

func (h *host) registerProvider(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	if err := registerProgram(h.database, store.KindProvider, params["name"], strings.Fields(params["command"]), nil); err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: fmt.Sprintf("registered %s; inspect it to see that it works and what it offers", params["name"])}, nil
}

func (h *host) configureProvider(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	program, err := configureProgram(h.database, store.KindProvider, params["provider"], []string{params["change"]})

	if err != nil {
		return protocol.HostResult{}, err
	}

	text := fmt.Sprintf("configuration of %s: %s", program.Name, program.Config)

	// A provider reads its configuration when it starts.
	if program.Name == h.settings.Provider && h.process != nil {
		_ = h.process.Close()
		h.process, h.worker = nil, nil
		text += "\nit was restarted, so the change applies to the next task"
	}

	return protocol.HostResult{Text: text}, nil
}

func (h *host) inspect(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	described, err := inspectProvider(h.database, params["provider"], h.conn.Log())

	if err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: described}, nil
}

func (h *host) unregisterProvider(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	if params["provider"] == h.settings.Provider {
		return protocol.HostResult{}, fmt.Errorf("%s is the provider in use; change to another one first", params["provider"])
	}

	if err := h.database.DeleteProgram(store.KindProvider, params["provider"]); err != nil {
		return protocol.HostResult{}, fmt.Errorf("provider %q is not registered", params["provider"])
	}

	return protocol.HostResult{Text: "removed the provider " + params["provider"]}, nil
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
