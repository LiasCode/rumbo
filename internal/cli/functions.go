package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

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
	// duringRun says the function answers while a run works even in a host
	// that works in one session at a time (-single): it only reads, or
	// stores what the run in progress already read when it started. There
	// the others wait for the run to end. In a host with several sessions, each function waits
	// only for the session it changes.
	duringRun bool
	// slow says the function reaches a provider, so it is answered without
	// holding up the requests that come after it.
	slow bool
	call func(h *host, ctx context.Context, params map[string]string) (protocol.HostResult, error)
}

// sessionParam lets a function act on a session other than the current one.
var sessionParam = protocol.Option{Name: "session", Type: protocol.OptionString, Values: "sessions", Optional: true}

// projectParam lets sessions list another project's, so a front end can
// show every project's sessions from one host.
var projectParam = protocol.Option{
	Name: "project", Type: protocol.OptionString, Values: "projects", Optional: true,
	Description: "the root of another project; without it, this one",
}

// chosenProvider lets a function ask a provider other than the current
// session's. A front end that lists a parameter's values with such a
// function passes the provider given before it, as use does.
var chosenProvider = protocol.Option{
	Name: "provider", Type: protocol.OptionString, Values: "providers", Optional: true,
	Description: "without it, the current session's",
}

// hostFunctions are listed to the front end in this order, by what they act
// on: the system, a session, and the providers.
var hostFunctions = []hostFunction{
	{
		Function: protocol.Function{
			Name:        "system",
			Description: "Show the system settings as they apply here, and where each value comes from",
			Group:       protocol.GroupSystem,
		},
		duringRun: true,
		call:      (*host).showSettings,
	},
	{
		Function: protocol.Function{
			Name:        "system-set",
			Description: "Store a system setting, which a session's own choice wins over",
			Params: []protocol.Option{
				{Name: "key", Type: protocol.OptionString, Values: "system-keys"},
				{Name: "value", Type: protocol.OptionString},
			},
			Group: protocol.GroupSystem,
		},
		duringRun: true,
		call:      (*host).storeSetting,
	},
	{
		Function: protocol.Function{
			Name:        "system-unset",
			Description: "Remove a stored system setting, so its default applies",
			Params: []protocol.Option{
				{Name: "key", Type: protocol.OptionString, Values: "system-keys"},
			},
			Group: protocol.GroupSystem,
		},
		duringRun: true,
		call:      (*host).removeSetting,
	},
	{
		Function:  protocol.Function{Name: "system-keys", Description: "List the system settings there are", Group: protocol.GroupSystem},
		duringRun: true,
		call:      (*host).settingKeys,
	},
	{
		Function:  protocol.Function{Name: "quit", Description: "Stop the providers and exit; runs in progress are cancelled", Group: protocol.GroupSystem},
		duringRun: true,
		call:      (*host).quit,
	},
	{
		Function: protocol.Function{
			Name:        "status",
			Description: "Show the session, the model, and what the conversation has cost",
			Params:      []protocol.Option{sessionParam},
			Group:       protocol.GroupSession,
		},
		duringRun: true,
		call:      (*host).status,
	},
	{
		Function: protocol.Function{
			Name:        "skills",
			Description: "List the skills the model is told of here, and why one is left out",
			Group:       protocol.GroupSession,
		},
		duringRun: true,
		call:      (*host).listSkills,
	},
	{
		Function: protocol.Function{
			Name:        "sessions",
			Description: "List the sessions of this project, or of another, most recently used first, with how each one stands",
			Params:      []protocol.Option{projectParam},
			Group:       protocol.GroupSession,
		},
		duringRun: true,
		call:      (*host).listSessions,
	},
	{
		Function:  protocol.Function{Name: "projects", Description: "List the projects that have sessions, most recently used first", Group: protocol.GroupSession},
		duringRun: true,
		call:      (*host).listProjects,
	},
	{
		Function: protocol.Function{
			Name:        "open",
			Description: "Continue another session of this project",
			Params:      []protocol.Option{{Name: "session", Type: protocol.OptionString, Values: "sessions"}},
			Group:       protocol.GroupSession,
		},
		call: (*host).openSession,
	},
	{
		Function: protocol.Function{Name: "new", Description: "Start a new session with the next task", Group: protocol.GroupSession},
		call:     (*host).newSession,
	},
	{
		Function: protocol.Function{
			Name:        "branch",
			Description: "Go on in a new session that starts from this one, as it is or as it was before one of your messages; this one stays as it is, and so do the files",
			Params: []protocol.Option{{
				Name: "point", Type: protocol.OptionString, Values: "branch-points", Optional: true,
				Description: "the number of one of your messages, which the branch starts before; without it, it starts from now",
			}},
			Group: protocol.GroupSession,
		},
		call: (*host).branchSession,
	},
	{
		Function:  protocol.Function{Name: "branch-points", Description: "List your messages in this session, which a branch can start before", Group: protocol.GroupSession},
		duringRun: true,
		call:      (*host).branchPoints,
	},
	{
		Function: protocol.Function{
			Name:        "delete",
			Description: "Delete a session of this project and its history",
			Params:      []protocol.Option{{Name: "session", Type: protocol.OptionString, Values: "sessions"}},
			Confirm:     "This deletes the session and everything recorded in it, for good. Files it changed stay as they are.",
			Group:       protocol.GroupSession,
		},
		call: (*host).deleteSession,
	},
	{
		Function: protocol.Function{
			Name:        "rename",
			Description: "Give this session a title of your own",
			Params:      []protocol.Option{{Name: "title", Type: protocol.OptionString}},
			Group:       protocol.GroupSession,
		},
		duringRun: true,
		call:      (*host).renameSession,
	},
	{
		Function: protocol.Function{
			Name:        "prune",
			Description: "List the sessions of this project not used in a given time, and delete them with their history",
			Params: []protocol.Option{
				{
					Name: "age", Type: protocol.OptionString,
					Description: "how long they have not been used: hours, days, or weeks, such as 12h, 30d, or 2w",
				},
				{
					Name: "confirm", Type: protocol.OptionString, Enum: []string{"yes"}, Optional: true,
					Description: "yes deletes them; without it, they are only listed",
				},
			},
			Group: protocol.GroupSession,
		},
		call: (*host).pruneSessions,
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
			Name:        "session-options",
			Description: "Show or change the option values chosen in this session, which it keeps, over its provider's configuration",
			Params:      []protocol.Option{{Name: "change", Type: protocol.OptionString, Values: "option-values", Description: "name=value; an empty value removes it", Optional: true}},
			Group:       protocol.GroupSession,
		},
		call: (*host).sessionOptions,
	},
	{
		Function: protocol.Function{
			Name:        "compact",
			Description: "Replace the conversation with a summary now, to free context",
			Params:      []protocol.Option{sessionParam},
			Group:       protocol.GroupSession,
		},
		slow: true,
		call: (*host).compactNow,
	},
	{
		Function:  protocol.Function{Name: "providers", Description: "List the providers there are, built in and registered", Group: protocol.GroupProvider},
		duringRun: true,
		call:      (*host).listProviders,
	},
	{
		Function: protocol.Function{
			Name:        "provider-models",
			Description: "List the models a provider offers, asking it",
			Params:      []protocol.Option{chosenProvider},
			Group:       protocol.GroupProvider,
		},
		duringRun: true,
		slow:      true,
		call:      (*host).providerModels,
	},
	{
		Function: protocol.Function{
			Name:        "option-values",
			Description: "List the values the options of a provider take, as name=value, asking it",
			Params:      []protocol.Option{chosenProvider},
			Group:       protocol.GroupProvider,
		},
		duringRun: true,
		slow:      true,
		call:      (*host).optionValues,
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
		duringRun: true,
		slow:      true,
		call:      (*host).registerProvider,
	},
	{
		Function: protocol.Function{
			Name:        "provider-config",
			Description: "Change a field of a provider's configuration, its options included; an empty value gives it back to its default",
			Params: []protocol.Option{
				{Name: "provider", Type: protocol.OptionString, Values: "providers"},
				{Name: "change", Type: protocol.OptionString, Values: "option-values", Description: "field=value"},
			},
			Group: protocol.GroupProvider,
		},
		call: (*host).configureProvider,
	},
	{
		Function: protocol.Function{
			Name:        "provider-show",
			Description: "Start a provider and show what it offers; also the test that it works",
			Params:      []protocol.Option{{Name: "provider", Type: protocol.OptionString, Values: "providers"}},
			Group:       protocol.GroupProvider,
		},
		slow: true,
		call: (*host).inspect,
	},
	{
		Function: protocol.Function{
			Name:        "provider-rm",
			Description: "Remove a provider you registered, or what you chose for a built-in one",
			Params:      []protocol.Option{{Name: "provider", Type: protocol.OptionString, Values: "providers"}},
			Confirm:     "This removes the provider's registration and its configuration. The program itself is not touched, and a built-in provider stays, as rumbo has it.",
			Group:       protocol.GroupProvider,
		},
		duringRun: true,
		call:      (*host).unregisterProvider,
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

	options, err := h.describeChoice(chosen)

	if err != nil {
		return protocol.HostResult{}, err
	}

	fmt.Fprintf(&out, "provider: %s\nmodel: %s\n%s\ndirectory: %s", chosen.provider, chosen.model, options, dir)

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

func (h *host) listSessions(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	project := params["project"]

	if project == "" {
		project = h.root
	}

	listed, err := standings(h.database, project)

	if err != nil {
		return protocol.HostResult{}, err
	}

	h.mu.Lock()
	working := map[string]string{}
	held := map[string]bool{h.current.id(): h.current.unlock != nil}

	for target, what := range h.busy {
		working[target.id()] = what
	}

	for id, target := range h.places {
		held[id] = held[id] || target.unlock != nil
	}

	h.mu.Unlock()

	var result protocol.HostResult

	for _, each := range listed {
		var parts []string

		switch {
		case working[each.ID] != "":
			parts = append(parts, working[each.ID]+" in progress")
		case each.inUse && !held[each.ID]:
			parts = append(parts, "in use by another rumbo process")
		default:
			parts = append(parts, each.ended())
		}

		parts = append(parts, counted(each.summary.Turns, "turn"))

		if each.Parent != "" {
			parts = append(parts, "branch of "+each.Parent)
		}

		parts = append(parts, "last used "+each.ActiveAt.Format("2006-01-02 15:04"))
		result.Values = append(result.Values, protocol.Value{Value: each.ID, Label: each.Title, Description: strings.Join(parts, "; ")})
	}

	if len(listed) == 0 && project == h.root {
		result.Text = "this project has no sessions yet"
	} else if len(listed) == 0 {
		result.Text = project + " has no sessions"
	}

	return result, nil
}

// listProjects lists the projects that have sessions, by their root, most
// recently used first, so a front end can show every project's sessions
// and start rumbo in the one a person opens.
func (h *host) listProjects(context.Context, map[string]string) (protocol.HostResult, error) {
	sessions, err := h.database.Sessions("")

	if err != nil {
		return protocol.HostResult{}, err
	}

	type project struct {
		root     string
		sessions int
		used     string
	}

	var projects []*project

	known := map[string]*project{}

	// The sessions come most recently used first, so a project's first one
	// says when it was last used.
	for _, session := range sessions {
		found, ok := known[session.Project]

		if !ok {
			found = &project{root: session.Project, used: session.ActiveAt.Format("2006-01-02 15:04")}
			known[session.Project] = found
			projects = append(projects, found)
		}

		found.sessions++
	}

	var result protocol.HostResult

	for _, project := range projects {
		description := counted(project.sessions, "session") + "; last used " + project.used

		if project.root == h.root {
			description = "this project; " + description
		}

		result.Values = append(result.Values, protocol.Value{Value: project.root, Label: filepath.Base(project.root), Description: description})
	}

	if len(projects) == 0 {
		result.Text = "no project has sessions yet"
	}

	return result, nil
}

// counted says how many of something there are, such as 1 turn or 2 turns.
func counted(count int, what string) string {
	if count == 1 {
		return "1 " + what
	}

	return fmt.Sprintf("%d %ss", count, what)
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

// renameSession gives the current session a title of the person's own.
func (h *host) renameSession(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	target := h.current

	if target.session == nil {
		return protocol.HostResult{}, errors.New("this session has no task yet: it takes its title from its first one, and can be renamed then")
	}

	title, err := retitle(h.database, target.session.ID, params["title"])

	if err != nil {
		return protocol.HostResult{}, err
	}

	// What works in the session keeps the copy it read; the place gets a
	// new one.
	renamed := *target.session
	renamed.Title = title
	target.session = &renamed

	return protocol.HostResult{Text: fmt.Sprintf("session %s is now %q", renamed.ID, title)}, nil
}

// pruneSessions lists the project's sessions not used in a while, and
// deletes them when confirmed. Those this host holds, or something works
// in, and those another rumbo process uses, are kept.
func (h *host) pruneSessions(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	age, err := parseAge(params["age"])

	if err != nil {
		return protocol.HostResult{}, err
	}

	confirmed := params["confirm"] == "yes"

	if params["confirm"] != "" && !confirmed {
		return protocol.HostResult{}, fmt.Errorf("confirm is yes, which deletes the sessions, or left out, which lists them; not %q", params["confirm"])
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	sessions, err := h.database.Sessions(h.root)

	if err != nil {
		return protocol.HostResult{}, err
	}

	old := unused(sessions, time.Now().Add(-age))

	if len(old) == 0 {
		return protocol.HostResult{Text: fmt.Sprintf("no session of this project was last used more than %s ago", params["age"])}, nil
	}

	if !confirmed {
		return h.prunable(old, params["age"])
	}

	var deleted, kept []string

	for _, session := range old {
		if h.holds(session.ID) {
			kept = append(kept, session.ID)
			continue
		}

		if err := discard(h.database, session.ID); errors.Is(err, store.ErrLocked) {
			kept = append(kept, session.ID)
			continue
		} else if err != nil {
			return protocol.HostResult{}, err
		}

		delete(h.places, session.ID)
		deleted = append(deleted, fmt.Sprintf("\n  %s  %s", session.ID, titled(session)))
	}

	text := fmt.Sprintf("deleted %s last used more than %s ago%s", counted(len(deleted), "session"), params["age"], strings.Join(deleted, ""))

	if len(kept) > 0 {
		text += fmt.Sprintf("\nkept %s in use: %s", counted(len(kept), "session"), strings.Join(kept, ", "))
	}

	return protocol.HostResult{Text: text}, nil
}

// prunable lists the sessions prune deletes when confirmed, and marks those
// it keeps. The caller holds the lock.
func (h *host) prunable(old []store.Session, age string) (protocol.HostResult, error) {
	var out strings.Builder

	fmt.Fprintf(&out, "%s of this project last used more than %s ago:", counted(len(old), "session"), age)

	for _, session := range old {
		used, err := h.database.SessionInUse(session.ID)

		if err != nil {
			return protocol.HostResult{}, err
		}

		mark := ""

		if used || h.holds(session.ID) {
			mark = "  (in use, kept)"
		}

		fmt.Fprintf(&out, "\n  %s  %s  %s%s", session.ID, session.ActiveAt.Format("2006-01-02 15:04"), titled(session), mark)
	}

	out.WriteString("\nwith confirm yes, prune deletes them with their history; those in use are kept")

	return protocol.HostResult{Text: out.String()}, nil
}

// holds says whether this host keeps a session from being deleted: it is
// the current one, the host holds it, or something works in it. The caller
// holds the lock.
func (h *host) holds(id string) bool {
	target, known := h.places[id]

	return id == h.current.id() || (known && (target.unlock != nil || h.busy[target] != ""))
}

func (h *host) newSession(context.Context, map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	previous := h.current
	h.current = h.fresh(previous)
	h.letGo(previous)

	return protocol.HostResult{Text: "the next task starts a new session" + h.background(previous)}, nil
}

// branchPoints lists the person's messages in the current session, by
// number, which a branch can start before.
func (h *host) branchPoints(context.Context, map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	session := h.current.session
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

// branchSession makes a branch of the current session the current one: a
// new session with a copy of its entries up to now, or up to just before
// one of the person's messages. The session it came from stays as it is,
// and goes on working in the background when it was. The files are not the
// session's, so they stay as they are.
func (h *host) branchSession(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	source := h.current

	if source.session == nil {
		return protocol.HostResult{}, errors.New("there is no session to branch yet")
	}

	points, err := h.points(source.session.ID)

	if err != nil {
		return protocol.HostResult{}, err
	}

	var upTo int64
	var before string

	switch point := params["point"]; point {
	case "":
		// What a run records while it works is no place to start from.
		if what := h.busy[source]; what != "" {
			return protocol.HostResult{}, fmt.Errorf("%s is busy with %s; branch from before one of your messages, or once it ends", source.name(), what)
		}

		entries, err := h.database.Entries(source.session.ID)

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

	created, err := h.database.BranchSession(source.session.ID, upTo)

	if err != nil {
		return protocol.HostResult{}, err
	}

	branch := h.fresh(source)
	branch.session, branch.dir = &created, created.Dir
	h.places[created.ID] = branch

	if err := h.choose(branch, source.choice); err != nil {
		return protocol.HostResult{}, err
	}

	h.current = branch
	h.letGo(source)
	h.prune()

	text := fmt.Sprintf("now in session %s, a branch of %s as it is", created.ID, source.session.ID)

	if before != "" {
		text = fmt.Sprintf("now in session %s, a branch of %s from before your message %s; the files are as they are now, not as they were then", created.ID, source.session.ID, params["point"])
	}

	text += h.background(source)

	if before != "" {
		text += "\nyour message was:\n" + before
	}

	return protocol.HostResult{Text: text}, nil
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

// useProvider changes the provider and the model of the current session,
// which keeps them from then on. The history is the host's, not the
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
	before := h.current.options
	options := before

	if name != h.current.provider {
		options = nil
	}

	if err := h.choose(h.current, choice{provider: name, model: params["model"], options: options}); err != nil {
		return protocol.HostResult{}, err
	}

	h.prune()

	text := fmt.Sprintf("this session now uses %s/%s", name, params["model"])

	if len(before) > 0 && len(options) == 0 {
		text += fmt.Sprintf("; the options chosen in it, %s, were for another provider and are dropped", before)
	}

	if h.busy[h.current] != "" {
		text += "; the run in progress keeps the provider it started with"
	}

	return protocol.HostResult{Text: text}, nil
}

// sessionOptions shows the option values chosen in the current session,
// after changing one when a change is given, and the configuration of its
// provider, which they win over. The session
// keeps what is chosen in it, as it keeps its model; a run in progress
// keeps the options it started with.
func (h *host) sessionOptions(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	target := h.current
	text := ""

	if params["change"] != "" {
		changed := target.choice
		options, err := changeObject(changed.options, []string{params["change"]})

		if err != nil {
			return protocol.HostResult{}, err
		}

		changed.options = options

		if err := h.choose(target, changed); err != nil {
			return protocol.HostResult{}, err
		}

		text = "kept for the session the next task starts; it applies to its tasks\n"

		if target.session != nil {
			text = "stored with this session; it applies to the tasks that start from now on\n"
		}
	}

	described, err := h.describeChoice(target.choice)

	if err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: text + described}, nil
}

// choose makes chosen what a place uses, and what its session keeps. A new
// session stores it when it is made. The caller holds the lock.
func (h *host) choose(target *place, chosen choice) error {
	if target.session == nil {
		target.choice = chosen
		return nil
	}

	if err := h.hold(target); err != nil {
		return err
	}

	before := target.choice
	target.choice = chosen

	if err := h.keep(target); err != nil {
		target.choice = before
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

func (h *host) showSettings(context.Context, map[string]string) (protocol.HostResult, error) {
	settings, err := effectiveSettings(h.database)

	if err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: "system settings:\n" + describeSettings(settings)}, nil
}

func (h *host) settingKeys(context.Context, map[string]string) (protocol.HostResult, error) {
	var result protocol.HostResult

	for _, spec := range settingSpecs {
		result.Values = append(result.Values, protocol.Value{Value: spec.key, Description: spec.help})
	}

	return result, nil
}

// storeSetting stores a system setting. Runs read the settings when they
// start, so what is stored applies to the next one, but for what rumbo
// reads when it starts.
func (h *host) storeSetting(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	if err := setSetting(h.database, params["key"], params["value"]); err != nil {
		return protocol.HostResult{}, err
	}

	spec, _ := findSetting(params["key"])
	text := fmt.Sprintf("system setting %s = %s, stored; %s", params["key"], params["value"], spec.applies())

	return protocol.HostResult{Text: text}, nil
}

func (h *host) removeSetting(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	if err := unsetSetting(h.database, params["key"]); err != nil {
		return protocol.HostResult{}, err
	}

	spec, _ := findSetting(params["key"])
	text := fmt.Sprintf("system setting %s is no longer stored; %s", params["key"], spec.applies())

	return protocol.HostResult{Text: text}, nil
}

func (h *host) registerProvider(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	if err := registerProvider(h.database, params["name"], strings.Fields(params["command"]), nil, h.conn.Log()); err != nil {
		return protocol.HostResult{}, err
	}

	text := fmt.Sprintf("registered the provider %s, which started; provider-show says its defaults and what it offers", params["name"])

	return protocol.HostResult{Text: text}, nil
}

func (h *host) configureProvider(_ context.Context, params map[string]string) (protocol.HostResult, error) {
	program, err := configureProgram(h.database, store.KindProvider, params["provider"], []string{params["change"]})

	if err != nil {
		return protocol.HostResult{}, err
	}

	text := fmt.Sprintf("configuration of %s: %s", program.Name, shown(program.Config))

	// A provider reads its configuration when it starts, so its processes
	// are replaced; one busy with a request stops when the request ends.
	// Its options are read from it when a run starts.
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.drop(program.Name) {
		text += "\nits processes were stopped, so the change applies to the next request"
	} else {
		text += "\nit applies to the runs that start from now on, in the sessions that choose no other value"
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

	removed, err := removeProgram(h.database, store.KindProvider, params["provider"])

	if err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Text: removed}, nil
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

	worker, err := h.agent(pool, chosen, nil, target.tools, h.conn.Report(session.ID))

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
