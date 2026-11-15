package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/internal/transcript"
	"github.com/LiasCode/rumbo/protocol"
)

// hostFunction is one thing a front end can ask of the host itself, whatever
// the provider. A front end learns them from the host and offers them
// without knowing them beforehand, the way it does with a provider's.
type hostFunction struct {
	protocol.Function
	// duringRun says the function answers while a run works even in a host
	// that works in one session at a time (-single): it only reads, or
	// stores what applies from the next start. There the others wait for
	// the run to end. In a host with several sessions, each function waits
	// only for the session it changes.
	duringRun bool
	// slow says the function reaches a provider, so it is answered without
	// holding up the requests that come after it.
	slow bool
	call func(h *host, ctx context.Context, params map[string]string) (protocol.HostResult, error)
}

// sessionParam lets a function act on a session other than the current one.
var sessionParam = protocol.Option{Name: "session", Type: protocol.OptionString, Values: "sessions", Optional: true}

// hostFunctions are listed to the front end in this order.
var hostFunctions = []hostFunction{
	{
		Function: protocol.Function{
			Name:        "status",
			Description: "Show the session, the model, and what the conversation has cost",
			Params:      []protocol.Option{sessionParam},
		},
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
			Description: "Change the provider and the model of this session for the tasks that follow",
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
		slow: true,
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
		Function: protocol.Function{
			Name:        "compact",
			Description: "Replace the conversation with a summary now, to free context",
			Params:      []protocol.Option{sessionParam},
		},
		slow: true,
		call: (*host).compactNow,
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

// slowFunction implements frontend.Handlers.Slow.
func slowFunction(name string) bool {
	index := slices.IndexFunc(hostFunctions, func(function hostFunction) bool { return function.Name == name })

	return index >= 0 && hostFunctions[index].slow
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

		if h.single && !function.duringRun && h.working() {
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
		Dir:      h.current.dir,
		Session:  h.current.id(),
		Provider: h.current.provider,
		Model:    h.current.model,
	}
}

// chosen returns the place a function acts on: the session it names, or
// the current one. The caller holds the lock.
func (h *host) chosen(params map[string]string) (*place, error) {
	if params["session"] == "" {
		return h.current, nil
	}

	return h.known(params["session"])
}

func (h *host) status(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	target, err := h.chosen(params)

	if err != nil {
		h.mu.Unlock()
		return protocol.HostResult{}, err
	}

	session, dir, chosen, working := target.session, target.dir, target.choice, h.busy[target]

	var others []string

	for other := range h.busy {
		if other != target && other.session != nil {
			others = append(others, other.session.ID)
		}
	}

	h.mu.Unlock()

	slices.Sort(others)

	var out strings.Builder

	fmt.Fprintf(&out, "provider: %s\nmodel: %s\ndirectory: %s", chosen.provider, chosen.model, dir)

	if len(chosen.options) > 0 {
		fmt.Fprintf(&out, "\noptions: %s", chosen.options)
	}

	if session == nil {
		out.WriteString("\nsession: none yet; the next task starts one")
	} else if err := h.describeSession(&out, *session, working); err != nil {
		return protocol.HostResult{}, err
	}

	if len(others) > 0 {
		fmt.Fprintf(&out, "\nworking here as well: %s", strings.Join(others, ", "))
	}

	return protocol.HostResult{Text: out.String()}, nil
}

// describeSession writes what a session holds: its context, what it has
// cost, and how its last run stands.
func (h *host) describeSession(out *strings.Builder, session store.Session, working string) error {
	entries, err := h.database.Entries(session.ID)

	if err != nil {
		return err
	}

	state, err := transcript.Build(entries)

	if err != nil {
		return err
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

	fmt.Fprintf(out, "\nsession: %s (%s)", session.ID, session.Title)
	fmt.Fprintf(out, "\ncontext: %d tokens", state.Tokens)

	if state.Window > 0 {
		fmt.Fprintf(out, " of %d, %d%%", state.Window, state.Tokens*100/state.Window)
	}

	if limit := h.settings.Compaction.Limit(state.Window); limit > 0 {
		fmt.Fprintf(out, "; summarized at %d", limit)
	}

	fmt.Fprintf(out, "\nspent: %d requests, %d input tokens, %d read from cache, %d output", turns, spent.Input, spent.CacheRead, spent.Output)

	// What the provider read from its cache is what the stable start of each
	// request saved; a share that drops says something broke the cache.
	if all := spent.Input + spent.CacheRead; all > 0 {
		fmt.Fprintf(out, "\nfrom the cache: %d%% of all input", spent.CacheRead*100/all)
	}

	switch {
	case working != "":
		fmt.Fprintf(out, "\n%s is in progress", working)
	case state.Unfinished:
		out.WriteString("\nthe last run did not finish, and can be resumed")
	}

	return nil
}

func (h *host) listSessions(context.Context, map[string]string) (protocol.HostResult, error) {
	sessions, err := h.database.Sessions(h.root)

	if err != nil {
		return protocol.HostResult{}, err
	}

	h.mu.Lock()
	working := map[string]string{}

	for target, what := range h.busy {
		working[target.id()] = what
	}

	h.mu.Unlock()

	var result protocol.HostResult

	for _, session := range sessions {
		description := "last used " + session.ActiveAt.Format("2006-01-02 15:04")

		if what := working[session.ID]; what != "" {
			description = what + " in progress; " + description
		}

		result.Values = append(result.Values, protocol.Value{Value: session.ID, Label: session.Title, Description: description})
	}

	if len(sessions) == 0 {
		result.Text = "this project has no sessions yet"
	}

	return result, nil
}

// openSession makes another session the current one. One that works goes
// on working in the background.
func (h *host) openSession(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	target, err := h.known(params["session"])

	if err != nil {
		return protocol.HostResult{}, err
	}

	if err := h.hold(target); err != nil {
		return protocol.HostResult{}, err
	}

	previous := h.current
	h.current = target
	h.letGo(previous)
	h.prune()

	text := fmt.Sprintf("continuing session %s (%s) in %s", target.session.ID, target.session.Title, target.dir)

	return protocol.HostResult{Text: text + h.background(previous)}, nil
}

// deleteSession removes a session for good. One that something works in,
// or that another rumbo process is using, is left alone.
func (h *host) deleteSession(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	target, err := h.known(params["session"])

	if err != nil {
		return protocol.HostResult{}, err
	}

	if what := h.busy[target]; what != "" {
		return protocol.HostResult{}, fmt.Errorf("%s is busy with %s; cancel it first", target.name(), what)
	}

	// The session has to be free: held here, or by no other process.
	if err := h.hold(target); err != nil {
		return protocol.HostResult{}, err
	}

	if err := h.database.DeleteSession(target.session.ID); err != nil {
		return protocol.HostResult{}, err
	}

	target.release()
	delete(h.places, target.session.ID)

	if target == h.current {
		h.current = h.fresh(target)
	}

	return protocol.HostResult{Text: fmt.Sprintf("deleted session %s (%s)", target.session.ID, target.session.Title)}, nil
}

func (h *host) newSession(context.Context, map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	previous := h.current
	h.current = h.fresh(previous)
	h.letGo(previous)

	return protocol.HostResult{Text: "the next task starts a new session" + h.background(previous)}, nil
}

// background says that a session left behind keeps working. The caller
// holds the lock.
func (h *host) background(previous *place) string {
	if what := h.busy[previous]; what != "" {
		return fmt.Sprintf("; %s goes on in %s", what, previous.name())
	}

	return ""
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

// useProvider changes the provider and the model of the current session,
// which keeps them from then on. The history is the host's, not the
// provider's, so the session goes on. A run in progress keeps the provider
// it started with.
func (h *host) useProvider(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	name := params["provider"]

	if _, err := h.database.Program(store.KindProvider, name); err != nil {
		return protocol.HostResult{}, fmt.Errorf("provider %q is not registered", name)
	}

	// Options given on the command line were meant for the provider rumbo
	// was started with, so only the stored ones carry over.
	options, err := storedOptions(h.database, h.root, name)

	if err != nil {
		return protocol.HostResult{}, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// A new session stores it when it is made.
	if session := h.current.session; session != nil {
		if err := h.database.SetSessionModel(session.ID, name, params["model"]); err != nil {
			return protocol.HostResult{}, err
		}

		session.Provider, session.Model = name, params["model"]
	}

	h.current.choice = choice{provider: name, model: params["model"], options: options}
	h.prune()

	text := fmt.Sprintf("using %s/%s", name, params["model"])

	if h.busy[h.current] != "" {
		text += "; the run in progress keeps the provider it started with"
	}

	return protocol.HostResult{Text: text}, nil
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

	h.mu.Lock()
	name := h.current.provider
	h.mu.Unlock()

	result.Values = append(result.Values, protocol.Value{
		Value:       optionsPrefix + name,
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

	// A provider reads its configuration when it starts, so its processes
	// are replaced; one busy with a request stops when the request ends.
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.drop(program.Name) {
		text += "\nits processes were stopped, so the change applies to the next request"
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
	h.mu.Lock()
	inUse := h.uses(params["provider"])
	h.mu.Unlock()

	if inUse {
		return protocol.HostResult{}, fmt.Errorf("%s is the provider in use; change to another one first", params["provider"])
	}

	if err := h.database.DeleteProgram(store.KindProvider, params["provider"]); err != nil {
		return protocol.HostResult{}, fmt.Errorf("provider %q is not registered", params["provider"])
	}

	return protocol.HostResult{Text: "removed the provider " + params["provider"]}, nil
}

// compactNow replaces a session's conversation with a summary. The session
// is busy meanwhile, so no run starts in it.
func (h *host) compactNow(ctx context.Context, params map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	target, err := h.chosen(params)

	switch {
	case err != nil:
	case target.session == nil:
		err = errors.New("there is no session to compact yet")
	case h.busy[target] != "":
		err = fmt.Errorf("%s is busy with %s; compact it when that ends", target.name(), h.busy[target])
	default:
		err = h.hold(target)
	}

	if err != nil {
		h.mu.Unlock()
		return protocol.HostResult{}, err
	}

	h.busy[target] = busyCompact
	chosen, session := target.choice, *target.session
	h.mu.Unlock()

	defer h.finish(target)

	pool, err := h.pool(chosen.provider)

	if err != nil {
		return protocol.HostResult{}, err
	}

	before, err := h.agent(pool, chosen, target.tools, h.conn.Report(session.ID)).Compact(ctx, session)

	if err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: fmt.Sprintf("the conversation, %d tokens, was replaced by a summary", before)}, nil
}

func (h *host) quit(context.Context, map[string]string) (protocol.HostResult, error) {
	h.conn.Stop()

	return protocol.HostResult{Text: "bye"}, nil
}
