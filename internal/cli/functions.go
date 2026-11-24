package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/LiasCode/rumbo/internal/frontends/render"
	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/internal/transcript"
	"github.com/LiasCode/rumbo/protocol"
)

// hostFunction is one thing a front end can ask of the host itself, whatever
// the provider. A front end learns them from the host and offers them
// without knowing them beforehand, the way it does with a provider's.
type hostFunction struct {
	protocol.Function
	// slow says the function reaches a provider, or waits for the system,
	// so it is answered without holding up the requests that come after it.
	slow bool
	call func(h *host, ctx context.Context, params map[string]string) (protocol.HostResult, error)
}

// chosenProvider lets a function ask a provider other than the session's. A front end that lists a parameter's values with such a
// function passes the provider given before it, as use does.
var chosenProvider = protocol.Option{
	Name: "provider", Type: protocol.OptionString, Values: "providers", Optional: true,
	Description: "without it, the session's",
}

// chosenExtension names the extension a function acts on.
var chosenExtension = protocol.Option{Name: "extension", Type: protocol.OptionString, Values: "extensions"}

// changeParam is the name=value change the functions that set something
// take; values names the function that lists the changes it takes.
func changeParam(values string) protocol.Option {
	return protocol.Option{Name: "change", Type: protocol.OptionString, Values: values, Description: "name=value; " + emptyGivesBack}
}

// hostFunctions are listed to the front end in this order, by what they act
// on: the system, a session, the providers, and the extensions.
var hostFunctions = []hostFunction{
	{
		Function: protocol.Function{
			Name:        "config",
			Description: "Show what is chosen and what applies here, this session first, each value with where it comes from",
			Group:       protocol.GroupSystem,
		},
		call: (*host).showConfig,
	},
	{
		Function: protocol.Function{
			Name:        "system-set",
			Description: "Change a system setting, which a session's own choice wins over",
			Params:      []protocol.Option{changeParam("system-values")},
			Group:       protocol.GroupSystem,
		},
		call: (*host).systemSet,
	},
	{
		Function: protocol.Function{Name: "system-values", Description: "List the changes system-set takes", Group: protocol.GroupSystem, Lists: true},
		call:     (*host).systemValues,
	},
	{
		Function: protocol.Function{
			Name:        "processes",
			Description: "Show this rumbo's processes, and the memory and processor time each uses",
			Group:       protocol.GroupSystem,
		},
		slow: true,
		call: (*host).showProcesses,
	},
	{
		Function: protocol.Function{Name: "quit", Description: "Stop the providers and exit; runs in progress are cancelled", Group: protocol.GroupSystem},
		call:     (*host).quit,
	},
	{
		Function: protocol.Function{
			Name:        "status",
			Description: "Show the session, the model, and what the conversation has cost",
			Group:       protocol.GroupSession,
		},
		call: (*host).status,
	},
	{
		Function: protocol.Function{
			Name:        "skills",
			Description: "List the skills the model is told of here, and why one is left out",
			Group:       protocol.GroupSession,
		},
		call: (*host).listSkills,
	},
	{
		Function: protocol.Function{
			Name:        "branch",
			Description: "Make a new session that starts from this one, as it is or as it was before one of your messages, for another rumbo to go on in; this one stays as it is, and so do the files",
			Params: []protocol.Option{{
				Name: "point", Type: protocol.OptionString, Values: "branch-points", Optional: true,
				Description: "the number of one of your messages, which the branch starts before; without it, it starts from now",
			}},
			Group: protocol.GroupSession,
		},
		call: (*host).branchSession,
	},
	{
		Function: protocol.Function{Name: "branch-points", Description: "List your messages in this session, which a branch can start before", Group: protocol.GroupSession, Lists: true},
		call:     (*host).branchPoints,
	},
	{
		Function: protocol.Function{
			Name:        "rename",
			Description: "Give this session a title of your own",
			Params:      []protocol.Option{{Name: "title", Type: protocol.OptionString}},
			Group:       protocol.GroupSession,
		},
		call: (*host).renameSession,
	},
	{
		Function: protocol.Function{
			Name:        "use",
			Description: "Change the provider and the model of this session for the tasks that follow",
			Params: []protocol.Option{
				{Name: "provider", Type: protocol.OptionString, Values: "providers"},
				{Name: "model", Type: protocol.OptionString, Values: "provider-models", Description: "A model of that provider"},
			},
			Group: protocol.GroupSession,
		},
		call: (*host).useProvider,
	},
	{
		Function: protocol.Function{
			Name:        "session-set",
			Description: "Choose a value of one of the provider's options for this session, which keeps it over the provider's own",
			Params:      []protocol.Option{changeParam("session-values")},
			Group:       protocol.GroupSession,
		},
		slow: true,
		call: (*host).sessionSet,
	},
	{
		Function: protocol.Function{Name: "session-values", Description: "List the changes session-set takes", Group: protocol.GroupSession, Lists: true},
		slow:     true,
		call:     (*host).sessionValues,
	},
	{
		Function: protocol.Function{
			Name:        "compact",
			Description: "Replace the conversation with a summary now, to free context",
			Group:       protocol.GroupSession,
		},
		slow: true,
		call: (*host).compactNow,
	},
	{
		Function: protocol.Function{Name: "providers", Description: "List the providers there are, those plugins bring and those registered", Group: protocol.GroupProvider},
		call:     (*host).listProviders,
	},
	{
		Function: protocol.Function{
			Name:        "provider-models",
			Description: "List the models a provider offers, asking it",
			Params:      []protocol.Option{chosenProvider},
			Group:       protocol.GroupProvider,
		},
		slow: true,
		call: (*host).providerModels,
	},
	{
		Function: protocol.Function{
			Name:        "provider-show",
			Description: "Start a provider and show each of its settings, with where its value comes from, and what it offers",
			Params:      []protocol.Option{chosenProvider},
			Group:       protocol.GroupProvider,
		},
		slow: true,
		call: (*host).providerShow,
	},
	{
		Function: protocol.Function{
			Name:        "provider-set",
			Description: "Set a provider's model, a field of its configuration, or one of its options, which it is started to check",
			Params: []protocol.Option{
				{Name: "provider", Type: protocol.OptionString, Values: "providers"},
				changeParam("provider-values"),
			},
			Group: protocol.GroupProvider,
		},
		slow: true,
		call: (*host).providerSet,
	},
	{
		Function: protocol.Function{
			Name:        "provider-values",
			Description: "List the changes provider-set takes",
			Params:      []protocol.Option{{Name: "provider", Type: protocol.OptionString, Values: "providers"}},
			Group:       protocol.GroupProvider,
			Lists:       true,
		},
		slow: true,
		call: (*host).providerValues,
	},
	{
		Function: protocol.Function{
			Name:        "provider-add",
			Description: "Register a provider: a name and the command that starts it, which is started to see that it works",
			Params: []protocol.Option{
				{Name: "name", Type: protocol.OptionString},
				{Name: "command", Type: protocol.OptionString, Description: "The program and its arguments, separated by spaces"},
			},
			Group: protocol.GroupProvider,
		},
		slow: true,
		call: (*host).registerProvider,
	},
	{
		Function: protocol.Function{
			Name:        "provider-rm",
			Description: "Remove a provider you registered, or what you set for one a plugin brings",
			Params:      []protocol.Option{{Name: "provider", Type: protocol.OptionString, Values: "providers"}},
			Confirm:     "This removes the provider's registration and its configuration. The program itself is not touched, and a provider a plugin brings stays, as the plugin has it.",
			Group:       protocol.GroupProvider,
		},
		call: (*host).unregisterProvider,
	},
	{
		Function: protocol.Function{Name: "extensions", Description: "List the extensions there are, those plugins bring and those registered", Group: protocol.GroupExtension},
		call:     (*host).listExtensions,
	},
	{
		Function: protocol.Function{
			Name:        "extension-show",
			Description: "Start an extension with the others and show what is set for it and what it gives the model, with what its tools add to every request",
			Params:      []protocol.Option{chosenExtension},
			Group:       protocol.GroupExtension,
		},
		slow: true,
		call: (*host).extensionShow,
	},
	{
		Function: protocol.Function{
			Name:        "extension-set",
			Description: "Set a field of an extension's configuration, or the tools of rumbo's it replaces, which it is started to check",
			Params:      []protocol.Option{chosenExtension, changeParam("extension-values")},
			Group:       protocol.GroupExtension,
		},
		slow: true,
		call: (*host).extensionSet,
	},
	{
		Function: protocol.Function{
			Name:        "extension-values",
			Description: "List the changes extension-set takes",
			Params:      []protocol.Option{chosenExtension},
			Group:       protocol.GroupExtension,
			Lists:       true,
		},
		call: (*host).extensionValues,
	},
	{
		Function: protocol.Function{
			Name:        "extension-add",
			Description: "Register an extension: a name and the command that starts it, which is started with the others to see that it works",
			Params: []protocol.Option{
				{Name: "name", Type: protocol.OptionString},
				{Name: "config", Type: protocol.OptionString, Optional: true, Description: `its configuration, as a JSON object, such as {"url":"https://example.com/mcp"}`},
				{Name: settingReplaces, Type: protocol.OptionString, Optional: true, Description: "the tools of rumbo's it takes the place of, separated by commas"},
				// Last, so a front end that reads typed words gives it the rest.
				{Name: "command", Type: protocol.OptionString, Description: "The program and its arguments, separated by spaces"},
			},
			Group: protocol.GroupExtension,
		},
		slow: true,
		call: (*host).registerExtension,
	},
	{
		Function: protocol.Function{
			Name:        "extension-rm",
			Description: "Remove an extension you registered, or what you set for one a plugin brings",
			Params:      []protocol.Option{chosenExtension},
			Confirm:     "This removes the extension's registration and its configuration, and its tools are gone from the next run. The program itself is not touched, and an extension a plugin brings stays, as the plugin has it.",
			Group:       protocol.GroupExtension,
		},
		call: (*host).unregisterExtension,
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

	state := protocol.HostState{Dir: h.dir, Provider: h.provider, Model: h.model}

	if h.session != nil {
		state.Session, state.Title = h.session.ID, h.session.Title
	}

	return state
}

func (h *host) status(context.Context, map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	session, chosen, working := h.session, h.choice, h.busy
	h.mu.Unlock()

	var out strings.Builder

	options, err := h.describeChoice(chosen)

	if err != nil {
		return protocol.HostResult{}, err
	}

	fmt.Fprintf(&out, "provider: %s\nmodel: %s\n%s\ndirectory: %s", chosen.provider, chosen.model, options, h.dir)

	if session == nil {
		out.WriteString("\nsession: none yet; the next task starts one")
	} else if err := h.describeSession(&out, *session, working); err != nil {
		return protocol.HostResult{}, err
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

	summary := transcript.Summarize(entries)
	spent := summary.Spent

	fmt.Fprintf(out, "\nsession: %s (%s)", session.ID, session.Title)

	if session.Parent != "" {
		fmt.Fprintf(out, "\na branch of %s, which it copied up to its entry %d", session.Parent, session.ParentSeq)
	}
	fmt.Fprintf(out, "\ncontext: %d tokens", state.Tokens)

	if state.Window > 0 {
		fmt.Fprintf(out, " of %d, %d%%", state.Window, state.Tokens*100/state.Window)
	}

	settings, err := loadLimits(h.database)

	if err != nil {
		return err
	}

	if limit := settings.Compaction.Limit(state.Window); limit > 0 {
		fmt.Fprintf(out, "; summarized at %d", limit)
	}

	fmt.Fprintf(out, "\nspent: %d requests, %d input tokens, %d read from cache, %d output", summary.Turns, render.Input(spent), spent.CacheRead, spent.Output)

	// What the provider read from its cache is what the stable start of each
	// request saved; a share that drops says something broke the cache.
	if all := render.Input(spent) + spent.CacheRead; all > 0 {
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

// renameSession gives the session a title of the person's own.
func (h *host) renameSession(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.session == nil {
		return protocol.HostResult{}, errors.New("this session has no task yet: it takes its title from its first one, and can be renamed then")
	}

	title, err := retitle(h.database, h.session.ID, params["title"])

	if err != nil {
		return protocol.HostResult{}, err
	}

	// What works in the session keeps the copy it read; the host gets a
	// new one.
	renamed := *h.session
	renamed.Title = title
	h.session = &renamed

	return protocol.HostResult{Text: fmt.Sprintf("session %s is now %q", renamed.ID, title)}, nil
}

// branchPoints lists the person's messages in the session, by number,
// which a branch can start before.
func (h *host) branchPoints(context.Context, map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	session := h.session
	h.mu.Unlock()

	if session == nil {
		return protocol.HostResult{Text: "this session has no messages yet"}, nil
	}

	points, err := h.points(session.ID)

	if err != nil {
		return protocol.HostResult{}, err
	}

	var result protocol.HostResult

	for index, point := range points {
		result.Values = append(result.Values, protocol.Value{Value: strconv.Itoa(index + 1), Label: render.FirstLine(point.Text)})
	}

	if len(points) == 0 {
		result.Text = "this session has no messages yet"
	}

	return result, nil
}

func (h *host) points(session string) ([]transcript.Point, error) {
	entries, err := h.database.Entries(session)

	if err != nil {
		return nil, err
	}

	return transcript.Points(entries)
}

// branchSession makes a branch of the session: a new session with a copy of
// its entries up to now, or up to just before one of the person's messages.
// This host stays in its session, which is left as it is, and answers with
// the branch, for another rumbo to go on in. The files are not the
// session's, so they stay as they are.
func (h *host) branchSession(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.session == nil {
		return protocol.HostResult{}, errors.New("there is no session to branch yet")
	}

	source := h.session.ID
	points, err := h.points(source)

	if err != nil {
		return protocol.HostResult{}, err
	}

	var upTo int64
	var before string

	switch point := params["point"]; point {
	case "":
		// What a run records while it works is no place to start from.
		if h.busy != "" {
			return protocol.HostResult{}, fmt.Errorf("%s is busy with %s; branch from before one of your messages, or once it ends", h.name(), h.busy)
		}

		entries, err := h.database.Entries(source)

		if err != nil {
			return protocol.HostResult{}, err
		}

		if len(entries) > 0 {
			upTo = entries[len(entries)-1].Seq
		}

	default:
		number, err := strconv.Atoi(point)

		if err != nil || number < 1 || number > len(points) {
			return protocol.HostResult{}, fmt.Errorf("there is no message %s in this session; branch-points lists them", point)
		}

		upTo, before = points[number-1].Seq-1, points[number-1].Text
	}

	created, err := h.database.BranchSession(source, upTo)

	if err != nil {
		return protocol.HostResult{}, err
	}

	text := fmt.Sprintf("session %s is a branch of this one as it is", created.ID)

	if before != "" {
		text = fmt.Sprintf("session %s is a branch of this one from before your message %s; the files are as they are now, not as they were then", created.ID, params["point"])
	}

	text += fmt.Sprintf("\ngo on in it with: rumbo -s %s", created.ID)

	if before != "" {
		text += "\nyour message was:\n" + before
	}

	return protocol.HostResult{Text: text, Values: []protocol.Value{{Value: created.ID, Label: created.Title}}}, nil
}

func (h *host) listProviders(context.Context, map[string]string) (protocol.HostResult, error) {
	programs, err := listPrograms(h.database, store.KindProvider)

	if err != nil {
		return protocol.HostResult{}, err
	}

	var result protocol.HostResult

	for _, program := range programs {
		result.Values = append(result.Values, protocol.Value{Value: program.Name, Description: strings.Join(program.Command, " ")})
	}

	return result, nil
}

// useProvider changes the provider and the model of the session, which
// keeps them from then on. The history is the host's, not the
// provider's, so the session goes on. A run in progress keeps the provider
// it started with.
func (h *host) useProvider(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	name := params["provider"]

	if _, err := findProgram(h.database, store.KindProvider, name); err != nil {
		return protocol.HostResult{}, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// The options chosen in a session are for its provider, so another one
	// starts without them.
	before := h.options
	options := before

	if name != h.provider {
		options = nil
	}

	if err := h.choose(choice{provider: name, model: params["model"], options: options}); err != nil {
		return protocol.HostResult{}, err
	}

	h.prune()

	text := fmt.Sprintf("this session now uses %s/%s", name, params["model"])

	if len(before) > 0 && len(options) == 0 {
		text += fmt.Sprintf("; the options chosen in it, %s, were for another provider and are dropped", before)
	}

	if h.busy != "" {
		text += "; the run in progress keeps the provider it started with"
	}

	return protocol.HostResult{Text: text}, nil
}

// sessionSet changes a value of an option chosen in the session,
// checked against what its provider describes, which is started for it. The
// session keeps what is chosen in it, as it keeps its model, over its
// provider's configuration; a run in progress keeps the options it started
// with.
func (h *host) sessionSet(ctx context.Context, params map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	provider := h.provider
	h.mu.Unlock()

	described, _, err := h.offers(ctx, provider)

	if err != nil {
		return protocol.HostResult{}, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// The provider was asked without the lock, and the session may have
	// moved to another one meanwhile.
	if h.provider != provider {
		return protocol.HostResult{}, fmt.Errorf("%s changed provider meanwhile, to %s; choose its options again", h.name(), h.provider)
	}

	// The change goes over what the session has now, not what it had when
	// this call began: a front end sends one change after another, and
	// this function is answered in the background, so they may overlap.
	options, said, err := chooseOptions(provider, described.Options, h.options, []string{params["change"]})

	if err != nil {
		return protocol.HostResult{}, err
	}

	changed := h.choice
	changed.options = options

	if err := h.choose(changed); err != nil {
		return protocol.HostResult{}, err
	}

	applies := "kept for the session the next task starts"

	if h.session != nil {
		applies = "kept with this session; it applies to the tasks that start from now on"
	}

	return protocol.HostResult{Text: strings.Join(said, "\n") + "\n" + applies}, nil
}

// choose makes chosen what the session uses, and what it keeps. A new
// session stores it when it is made. The caller holds the lock.
func (h *host) choose(chosen choice) error {
	if h.session == nil {
		h.choice = chosen
		return nil
	}

	if err := h.hold(); err != nil {
		return err
	}

	before := h.choice
	h.choice = chosen

	if err := h.keep(); err != nil {
		h.choice = before
		return err
	}

	return nil
}

// describeChoice says what a run with chosen gets beyond its model, one
// level to a line: the option values chosen in the session, and under them
// the configuration of its provider. What neither holds is the provider's
// own default, which only the provider knows.
func (h *host) describeChoice(chosen choice) (string, error) {
	program, err := findProgram(h.database, store.KindProvider, chosen.provider)

	if err != nil {
		return "", err
	}

	options := "none"

	if len(chosen.options) > 0 {
		options = string(chosen.options)
	}

	return fmt.Sprintf("options chosen in the session: %s\nconfiguration of %s, under them: %s", options, chosen.provider, shown(program.Config)), nil
}

// showConfig shows what is chosen and what applies, the session's choice
// first, since it wins there.
func (h *host) showConfig(context.Context, map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	layer := sessionLayer{title: "this session, which is new", choice: h.choice}

	if h.session != nil {
		layer.title = "this session, " + h.session.ID
	}

	h.mu.Unlock()

	text, err := describeConfig(h.database, &layer, "provider-show")

	if err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: text}, nil
}

// systemSet changes a system setting. Runs read the settings when they
// start, so what is set applies to the next one, but for what rumbo reads
// when it starts.
func (h *host) systemSet(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	said, err := setSettings(h.database, []string{params["change"]})

	if err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: strings.Join(said, "\n")}, nil
}

func (h *host) registerProvider(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	if err := registerProvider(h.database, params["name"], strings.Fields(params["command"]), nil, h.conn.Log()); err != nil {
		return protocol.HostResult{}, err
	}

	text := fmt.Sprintf("registered the provider %s, which started; provider-show says its settings and what it offers", params["name"])

	return protocol.HostResult{Text: text}, nil
}

// providerSet changes a setting of a provider, which is started to check
// it. A provider reads its configuration when it starts, so its processes
// are replaced; one busy with a request stops when the request ends. A run
// reads the provider's options and model when it starts.
func (h *host) providerSet(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	// It is answered in the background, and reads what is kept to change
	// it: one call at a time, so two do not lose each other's change.
	h.setting.Lock()
	defer h.setting.Unlock()

	name := params["provider"]
	said, err := setProgram(h.database, store.KindProvider, name, []string{params["change"]}, h.conn.Log())

	if err != nil {
		return protocol.HostResult{}, err
	}

	text := strings.Join(said, "\n") + "\nkept in " + configFile + "; it applies to the runs that start from now on"

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.drop(name) {
		text += ", and its processes were stopped to take it"
	}

	return protocol.HostResult{Text: text}, nil
}

// providerShow starts a provider and shows its settings: the one named, or
// the session's, with what the session chose over the rest.
func (h *host) providerShow(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	chosen := h.choice
	h.mu.Unlock()

	name := params["provider"]

	if name == "" {
		name = chosen.provider
	}

	text, err := showProvider(h.database, name, &chosen, h.conn.Log())

	if err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: text}, nil
}

func (h *host) unregisterProvider(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	inUse := h.provider == params["provider"]
	h.mu.Unlock()

	if inUse {
		return protocol.HostResult{}, fmt.Errorf("%s is the provider in use; change to another one first", params["provider"])
	}

	removed, err := removeProgram(h.database, store.KindProvider, params["provider"])

	if err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: removed}, nil
}

func (h *host) listExtensions(context.Context, map[string]string) (protocol.HostResult, error) {
	programs, err := listPrograms(h.database, store.KindExtension)

	if err != nil {
		return protocol.HostResult{}, err
	}

	if len(programs) == 0 {
		return protocol.HostResult{Text: "there are no extensions; extension-add registers one"}, nil
	}

	var result protocol.HostResult

	for _, program := range programs {
		result.Values = append(result.Values, protocol.Value{Value: program.Name, Description: strings.Join(program.Command, " ")})
	}

	return result, nil
}

// extensionShow starts the extensions and says what one gives the model,
// in processes of its own, so the host's are not touched.
func (h *host) extensionShow(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	text, err := showExtension(h.database, params["extension"], h.conn.LogExtension("extensions"))

	if err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: text}, nil
}

// extensionSet changes what is set for an extension, which is started with
// the others to check it. The host's process of it is started again with
// what is kept when a run next starts.
func (h *host) extensionSet(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	// It is answered in the background, and reads what is kept to change
	// it: one call at a time, so two do not lose each other's change.
	h.setting.Lock()
	defer h.setting.Unlock()

	said, err := setProgram(h.database, store.KindExtension, params["extension"], []string{params["change"]}, h.conn.LogExtension("extensions"))

	if err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: strings.Join(said, "\n") + "\nkept in " + configFile + "; it applies to the runs that start from now on"}, nil
}

func (h *host) registerExtension(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	h.setting.Lock()
	defer h.setting.Unlock()

	var config json.RawMessage

	if params["config"] != "" {
		config = json.RawMessage(params["config"])
	}

	added, err := registerExtension(h.database, params["name"], strings.Fields(params["command"]), config, replacing(params[settingReplaces]), h.conn.LogExtension("extensions"))

	if err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: added}, nil
}

// unregisterExtension takes out what the user registered of an extension.
// The host's process of it stops when a run next starts.
func (h *host) unregisterExtension(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	removed, err := removeProgram(h.database, store.KindExtension, params["extension"])

	if err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: removed}, nil
}

// compactNow replaces the session's conversation with a summary. The
// session is busy meanwhile, so no run starts in it.
func (h *host) compactNow(ctx context.Context, _ map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()

	var err error

	switch {
	case h.session == nil:
		err = errors.New("there is no session to compact yet")
	case h.busy != "":
		err = fmt.Errorf("%s is busy with %s; compact it when that ends", h.name(), h.busy)
	default:
		err = h.hold()
	}

	if err != nil {
		h.mu.Unlock()
		return protocol.HostResult{}, err
	}

	h.busy = busyCompact
	chosen, session := h.choice, *h.session
	h.mu.Unlock()

	defer h.finish()

	pool, err := h.ready(ctx, chosen.provider)

	if err != nil {
		return protocol.HostResult{}, err
	}

	worker, err := h.agent(ctx, pool, chosen, nil, h.conn.Report(session.ID))

	if err != nil {
		return protocol.HostResult{}, err
	}

	before, err := worker.Compact(ctx, session)

	if err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: fmt.Sprintf("the conversation, %d tokens, was replaced by a summary", before)}, nil
}

func (h *host) quit(context.Context, map[string]string) (protocol.HostResult, error) {
	h.conn.Stop()

	return protocol.HostResult{Text: "bye"}, nil
}
