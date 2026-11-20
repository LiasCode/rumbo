package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LiasCode/rumbo/internal/provider"
	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/internal/tool"
	"github.com/LiasCode/rumbo/internal/transcript"
	"github.com/LiasCode/rumbo/protocol"
)

func testHost(t *testing.T) *host {
	t.Helper()

	database := store.Open(filepath.Join(t.TempDir(), configFile), t.TempDir())

	database.PutProgram(store.KindProvider, store.Program{Name: "first", Command: []string{"first-provider"}, Model: "small"})
	database.PutProgram(store.KindProvider, store.Program{Name: "second", Command: []string{"second-provider"}, Config: json.RawMessage(`{"depth":2,"base_url":"http://second"}`)})

	return hostOn(t, database)
}

// withClaude registers claude, a provider the test binary serves as the
// built-in anthropic, which describes one option, effort, with the values it
// takes. Unless config says otherwise, it reaches a closed port on this
// machine, so nothing a test does reaches Anthropic, whatever credentials
// the machine has.
func withClaude(t *testing.T, database store.Store, config string) {
	t.Helper()

	merged, err := mergeOptions(json.RawMessage(`{"base_url":"http://127.0.0.1:9"}`), json.RawMessage(config))

	if err != nil {
		t.Fatal(err)
	}

	program := store.Program{Name: "claude", Command: []string{selfPlaceholder, "provide", "anthropic"}, Config: merged}

	if err := database.PutProgram(store.KindProvider, program); err != nil {
		t.Fatal(err)
	}
}

// effort is what claude says of its options, for working out the values in
// force without starting it.
var effort = protocol.Description{Options: []protocol.Option{{Name: "effort", Type: protocol.OptionString}}}

// depth is what a provider says of itself when it takes one option, depth,
// which is what tells it from the rest of the provider's configuration.
var depth = protocol.Description{Options: []protocol.Option{{Name: "depth", Type: protocol.OptionNumber}}}

// hostOn is a host on database, as a new start of rumbo makes one: its
// settings choose the provider first and the model small.
func hostOn(t *testing.T, database store.Store) *host {
	t.Helper()

	tools := tool.NewSet()

	created := &host{
		database: database,
		root:     "/repo",
		tools:    tools,
		settings: config{DefaultProvider: "first"},
		current: &place{
			choice: choice{provider: "first", model: "small"},
			dir:    "/repo",
			tools:  tools.Separate(),
		},
		places: map[string]*place{},
		busy:   map[*place]string{},
		pools:  map[string]*provider.Pool{},
	}

	t.Cleanup(created.close)

	return created
}

func call(t *testing.T, h *host, name, params string) protocol.HostResult {
	t.Helper()

	result, err := h.invokeHost(context.Background(), name, []byte(params))

	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}

	return result
}

// refused calls a function that must fail, and returns its error.
func refused(t *testing.T, h *host, name, params string) string {
	t.Helper()

	_, err := h.invokeHost(context.Background(), name, []byte(params))

	if err == nil {
		t.Fatalf("%s %s did not fail", name, params)
	}

	return err.Error()
}

func TestHostListsEveryProjectAndTheSessionsOfAny(t *testing.T) {
	h := testHost(t)

	if none := call(t, h, "projects", ""); len(none.Values) != 0 || none.Text == "" {
		t.Fatalf("projects with no sessions = %+v", none)
	}

	h.database.CreateSession("/repo", "/repo", "here")
	h.database.CreateSession("/elsewhere/sub", "/elsewhere", "there")
	there, _ := h.database.CreateSession("/elsewhere", "/elsewhere", "there too")

	projects := call(t, h, "projects", "").Values

	if len(projects) != 2 {
		t.Fatalf("projects = %+v", projects)
	}

	// Most recently used first, each by its root, with how many sessions it
	// has; this host's own says so.
	if projects[0].Value != "/elsewhere" || projects[0].Label != "elsewhere" || !strings.HasPrefix(projects[0].Description, "2 sessions; last used ") {
		t.Fatalf("first project = %+v", projects[0])
	}

	if projects[1].Value != "/repo" || !strings.HasPrefix(projects[1].Description, "this project; 1 session; ") {
		t.Fatalf("second project = %+v", projects[1])
	}

	elsewhere := call(t, h, "sessions", `{"project":"/elsewhere"}`).Values

	if len(elsewhere) != 2 || elsewhere[0].Value != there.ID || elsewhere[0].Label != "there too" {
		t.Fatalf("sessions of /elsewhere = %+v", elsewhere)
	}

	if own := call(t, h, "sessions", "").Values; len(own) != 1 || own[0].Label != "here" {
		t.Fatalf("sessions of this project = %+v", own)
	}

	if empty := call(t, h, "sessions", `{"project":"/nowhere"}`); len(empty.Values) != 0 || !strings.Contains(empty.Text, "/nowhere") {
		t.Fatalf("sessions of an unknown project = %+v", empty)
	}

	// Listing another project's sessions does not move this host there.
	if state := call(t, h, "status", "").State; state.Dir != "/repo" {
		t.Fatalf("state after listing = %+v", state)
	}
}

func TestHostFunctionsChangeSessionAndProvider(t *testing.T) {
	h := testHost(t)
	older, _ := h.database.CreateSession("/repo/sub", "/repo", "older work")
	h.database.CreateSession("/elsewhere", "/elsewhere", "another project")
	foreign, _ := h.database.Sessions("/elsewhere")

	listed := call(t, h, "sessions", "")

	if len(listed.Values) != 1 || listed.Values[0].Value != older.ID || listed.Values[0].Label != "older work" {
		t.Fatalf("sessions = %+v", listed.Values)
	}

	// Opening a session attaches the front end to it and to where it works.
	opened := call(t, h, "open", `{"session":"`+older.ID+`"}`)

	if opened.State.Session != older.ID || opened.State.Dir != "/repo/sub" || h.current.session == nil {
		t.Fatalf("after open: %+v", opened.State)
	}

	if _, err := h.database.LockSession(older.ID); err == nil {
		t.Fatal("the opened session is not held by this host")
	}

	if err := refused(t, h, "open", `{"session":"`+foreign[0].ID+`"}`); !strings.Contains(err, "belongs to /elsewhere") {
		t.Fatalf("opening a session of another project: err = %v", err)
	}

	if status := call(t, h, "status", ""); !strings.Contains(status.Text, "session: "+older.ID) || !strings.Contains(status.Text, "context: 0 tokens") {
		t.Fatalf("status = %q", status.Text)
	}

	if fresh := call(t, h, "new", ""); fresh.State.Session != "" || h.current.session != nil {
		t.Fatalf("after new: %+v", fresh.State)
	}

	if unlock, err := h.database.LockSession(older.ID); err != nil {
		t.Fatal("the session left behind is still held")
	} else {
		unlock()
	}

	// Deleting removes the session and its history, and nothing else.
	kept, _ := h.database.CreateSession("/repo", "/repo", "kept")
	h.database.Append(older.ID, "user", map[string]string{"text": "hi"})

	refused(t, h, "delete", `{"session":"`+foreign[0].ID+`"}`)

	held, _ := h.database.LockSession(kept.ID)

	if err := refused(t, h, "delete", `{"session":"`+kept.ID+`"}`); !strings.Contains(err, "in use") {
		t.Fatalf("deleting a session in use elsewhere: err = %v", err)
	}

	held()

	if deleted := call(t, h, "delete", `{"session":"`+older.ID+`"}`); !strings.Contains(deleted.Text, "older work") {
		t.Fatalf("delete = %q", deleted.Text)
	}

	if entries, _ := h.database.Entries(older.ID); len(entries) != 0 {
		t.Fatal("the entries of a deleted session are still there")
	}

	if remaining := call(t, h, "sessions", ""); len(remaining.Values) != 1 || remaining.Values[0].Value != kept.ID {
		t.Fatalf("after delete: %+v", remaining.Values)
	}

	// Deleting the session in use leaves the front end without one.
	call(t, h, "open", `{"session":"`+kept.ID+`"}`)

	if gone := call(t, h, "delete", `{"session":"`+kept.ID+`"}`); gone.State.Session != "" || h.current.session != nil {
		t.Fatalf("after deleting the open session: %+v", gone.State)
	}

	// The providers are the built-in ones and those registered, by name.
	if providers := call(t, h, "providers", ""); shownValues(providers.Values) != "anthropic: | chatgpt: | first: | openai: | second:" {
		t.Fatalf("providers = %+v", providers.Values)
	}

	// Changing provider brings the options that provider's configuration
	// gives, not the ones in force for the one before.
	used := call(t, h, "use", `{"provider":"second","model":"large"}`)

	if options, _ := h.inForce(h.current.choice, depth); used.State.Provider != "second" || used.State.Model != "large" || string(options) != `{"depth":2}` {
		t.Fatalf("after use: %+v, options %s", used.State, options)
	}

	for name, params := range map[string]string{"use": `{"provider":"missing","model":"x"}`, "open": `{}`, "nope": ``} {
		refused(t, h, name, params)
	}
}

// working makes the host's current session one where a run works.
func working(t *testing.T, h *host) store.Session {
	t.Helper()

	session, _ := h.database.CreateSession("/repo", "/repo", "in progress")
	h.database.Append(session.ID, "user", map[string]string{"text": "the objective"})

	h.current.session = &session
	h.places[session.ID] = h.current
	h.busy[h.current] = busyRun

	if err := h.hold(h.current); err != nil {
		t.Fatal(err)
	}

	return session
}

// With several sessions, a function waits only for the session it changes:
// the front end can move to another session while a run goes on.
func TestFunctionsWaitOnlyForTheSessionTheyChange(t *testing.T) {
	h := testHost(t)
	session := working(t, h)
	running := h.current
	other, _ := h.database.CreateSession("/repo/sub", "/repo", "other work")

	// The run has no ending yet because it is still going, not because it
	// was interrupted.
	if status := call(t, h, "status", ""); !strings.Contains(status.Text, "a run is in progress") || strings.Contains(status.Text, "resumed") {
		t.Fatalf("status during a run = %q", status.Text)
	}

	if err := refused(t, h, "compact", ""); !strings.Contains(err, "busy with a run") {
		t.Fatalf("compacting the session that works: err = %v", err)
	}

	if err := refused(t, h, "delete", `{"session":"`+session.ID+`"}`); !strings.Contains(err, "busy with a run") {
		t.Fatalf("deleting the session that works: err = %v", err)
	}

	// The session's provider changes for the runs that follow it.
	if used := call(t, h, "use", `{"provider":"second","model":"large"}`); !strings.Contains(used.Text, "keeps the provider it started with") || h.current.provider != "second" {
		t.Fatalf("use during a run = %q", used.Text)
	}

	// Moving to another session leaves the run working in the background.
	if opened := call(t, h, "open", `{"session":"`+other.ID+`"}`); !strings.Contains(opened.Text, "a run goes on in session "+session.ID) || opened.State.Session != other.ID {
		t.Fatalf("open during a run = %+v", opened)
	}

	// A session this host has not worked in starts with the provider of
	// the current one; the one left behind keeps its own.
	if h.current.provider != "second" || running.provider != "second" {
		t.Fatalf("providers: current %s, running %s", h.current.provider, running.provider)
	}

	call(t, h, "use", `{"provider":"first","model":"small"}`)

	if running.provider != "second" {
		t.Fatal("use changed a session other than the current one")
	}

	for _, listed := range call(t, h, "sessions", "").Values {
		if marked := strings.Contains(listed.Description, "a run in progress"); marked != (listed.Value == session.ID) {
			t.Fatalf("sessions: %+v", listed)
		}
	}

	if status := call(t, h, "status", `{"session":"`+session.ID+`"}`); !strings.Contains(status.Text, "a run is in progress") {
		t.Fatalf("status of the session that works = %q", status.Text)
	}

	if status := call(t, h, "status", ""); !strings.Contains(status.Text, "working here as well: "+session.ID) {
		t.Fatalf("status of the current session = %q", status.Text)
	}

	// A run in the session that works is refused; one in a new session is not.
	if _, err := h.accept(protocol.RunParams{Objective: "more", Session: session.ID}, nil); err == nil || !strings.Contains(err.Error(), "busy with a run") {
		t.Fatalf("a second run in a busy session: err = %v", err)
	}

	if _, err := h.accept(protocol.RunParams{Objective: "elsewhere", New: true}, nil); err != nil {
		t.Fatalf("a run in a new session: %v", err)
	}

	// The host holds the session that works until the run ends, and then
	// lets it go, since it is no longer the current one.
	if _, err := h.database.LockSession(session.ID); err == nil {
		t.Fatal("the session that works is not held by this host")
	}

	h.finish(running)

	if unlock, err := h.database.LockSession(session.ID); err != nil {
		t.Fatal("the session is still held after its run ended")
	} else {
		unlock()
	}
}

// A host that works in one session at a time (-single) behaves as hosts did
// before there could be several: what changes the session or the provider
// waits for the run, and runs go only to the current session.
func TestSingleHostWaitsForTheRun(t *testing.T) {
	h := testHost(t)
	h.single = true
	working(t, h)
	other, _ := h.database.CreateSession("/repo", "/repo", "other work")

	call(t, h, "status", "")
	call(t, h, "sessions", "")

	for name, params := range map[string]string{
		"open":    `{"session":"` + other.ID + `"}`,
		"new":     ``,
		"use":     `{"provider":"second","model":"large"}`,
		"compact": ``,
		"delete":  `{"session":"` + other.ID + `"}`,
	} {
		if err := refused(t, h, name, params); !strings.Contains(err, "waits for the run in progress to end") {
			t.Fatalf("%s during a run: err = %v", name, err)
		}
	}

	for _, params := range []protocol.RunParams{{Objective: "x", New: true}, {Objective: "x", Session: other.ID}} {
		if _, err := h.accept(params, nil); err == nil || !strings.Contains(err.Error(), "-single") {
			t.Fatalf("run %+v: err = %v", params, err)
		}
	}

	if _, err := h.accept(protocol.RunParams{Objective: "x"}, nil); err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("a second run: err = %v", err)
	}

	if h.current.provider != "first" {
		t.Fatalf("a refused function changed the host: provider %s", h.current.provider)
	}
}

// A session keeps its provider from one start of rumbo to the next: the one
// it was made with, or the one use chose for it.
func TestSessionsKeepTheirProviderAcrossStarts(t *testing.T) {
	h := testHost(t)

	made, err := h.made(h.current, "first task")

	if err != nil {
		t.Fatal(err)
	}

	if stored, _ := h.database.Session(made.ID); stored.Provider != "first" || stored.Model != "small" {
		t.Fatalf("a new session stored %s/%s", stored.Provider, stored.Model)
	}

	call(t, h, "use", `{"provider":"second","model":"large"}`)
	older, _ := h.database.CreateSession("/repo", "/repo", "from before providers were stored")
	h.close()

	// A later start, whose settings say first/small, finds each session with
	// its own provider; one without a stored provider takes the current one.
	later := hostOn(t, h.database)

	opened := call(t, later, "open", `{"session":"`+made.ID+`"}`)

	if options, _ := later.inForce(later.current.choice, depth); opened.State.Provider != "second" || opened.State.Model != "large" || string(options) != `{"depth":2}` {
		t.Fatalf("after a restart: %+v, options %s", opened.State, options)
	}

	if opened := call(t, later, "open", `{"session":"`+older.ID+`"}`); opened.State.Provider != "second" {
		t.Fatalf("a session with no stored provider: %+v", opened.State)
	}
}

// A session keeps the options chosen in it, as it keeps its model, and goes
// without them when it changes to another provider. What is chosen is
// checked against what the provider describes.
func TestSessionsKeepTheOptionsChosenInThem(t *testing.T) {
	h := testHost(t)
	withClaude(t, h.database, `{"effort":"high"}`)
	call(t, h, "use", `{"provider":"claude","model":"claude-x"}`)

	// Chosen before the session is made, they wait for it, over those of
	// the provider's configuration.
	if chosen := call(t, h, "session-set", `{"change":"effort=low"}`); chosen.Text != "effort = low\nkept for the session the next task starts" {
		t.Fatalf("session-set = %q", chosen.Text)
	}

	if options, _ := h.inForce(h.current.choice, effort); string(options) != `{"effort":"low"}` {
		t.Fatalf("options in force = %s", options)
	}

	for change, wrong := range map[string]string{
		"effrt=low":   `claude has no option "effrt"; its options are effort`,
		"effort=huge": `effort is one of low, medium, high, xhigh, max, not "huge"`,
		"effort":      `"effort" is not name=value`,
	} {
		if err := refused(t, h, "session-set", `{"change":"`+change+`"}`); !strings.Contains(err, wrong) {
			t.Errorf("session-set %s: %s, want %q", change, err, wrong)
		}
	}

	made, err := h.made(h.current, "first task")

	if err != nil {
		t.Fatal(err)
	}

	if chosen := call(t, h, "session-set", `{"change":"effort=max"}`); !strings.Contains(chosen.Text, "kept with this session") {
		t.Fatalf("session-set in a session = %q", chosen.Text)
	}

	if stored, _ := h.database.Session(made.ID); string(stored.Options) != `{"effort":"max"}` {
		t.Fatalf("stored options = %s", stored.Options)
	}

	h.close()

	later := hostOn(t, h.database)
	call(t, later, "open", `{"session":"`+made.ID+`"}`)

	for _, said := range []string{
		`options chosen in the session: {"effort":"max"}`,
		`configuration of claude, under them: {"base_url":"http://127.0.0.1:9","effort":"high"}`,
	} {
		if status := call(t, later, "status", ""); !strings.Contains(status.Text, said) {
			t.Fatalf("status after a restart = %q, missing %q", status.Text, said)
		}
	}

	// An empty value gives one back to the provider's, and only one chosen.
	if back := call(t, later, "session-set", `{"change":"effort="}`); !strings.HasPrefix(back.Text, "effort: given back to claude's") || later.current.options != nil {
		t.Fatalf("session-set effort= = %q, options %s", back.Text, later.current.options)
	}

	if err := refused(t, later, "session-set", `{"change":"effort="}`); !strings.Contains(err, "not chosen in the session") {
		t.Fatalf("giving back what is not chosen: %s", err)
	}

	// Another provider does not get them.
	call(t, later, "session-set", `{"change":"effort=low"}`)
	used := call(t, later, "use", `{"provider":"first","model":"small"}`)

	if !strings.Contains(used.Text, "were for another provider and are dropped") || later.current.options != nil {
		t.Fatalf("use = %q, options %s", used.Text, later.current.options)
	}

	if stored, _ := later.database.Session(made.ID); stored.Options != nil {
		t.Fatalf("options left stored = %s", stored.Options)
	}
}

// A front end sends one change after another, and session-set is answered
// in the background, so two can overlap: each goes over what the session has
// then, and neither is lost.
func TestOverlappingChangesOfASessionAreAllKept(t *testing.T) {
	h := testHost(t)
	h.database.PutProgram(store.KindProvider, store.Program{Name: "two", Command: []string{selfPlaceholder, "provide", "two-options"}})
	call(t, h, "use", `{"provider":"two","model":"any"}`)

	// Nothing of the provider runs yet, so each call starts a process of
	// its own, which is slow: the two are under way at once.
	var both sync.WaitGroup

	for _, change := range []string{"effort=high", "note=hey"} {
		both.Go(func() { call(t, h, "session-set", `{"change":"`+change+`"}`) })
	}

	both.Wait()

	if string(h.current.options) != `{"effort":"high","note":"hey"}` {
		t.Fatalf("the session kept %s", h.current.options)
	}
}

// What is set applies to the next run, without starting rumbo again; what
// rumbo reads when it starts says so.
func TestStoredSettingsApplyToTheNextRun(t *testing.T) {
	h := testHost(t)
	withClaude(t, h.database, "")
	call(t, h, "use", `{"provider":"claude","model":"claude-x"}`)

	stored := call(t, h, "system-set", `{"change":"max_turns=7"}`)

	if stored.Text != "max_turns = 7, to the runs that start from now on" {
		t.Fatalf("system-set max_turns = %q", stored.Text)
	}

	if set := call(t, h, "provider-set", `{"provider":"claude","change":"effort=low"}`); set.Text != "effort = low\nkept in config.yaml; it applies to the runs that start from now on" {
		t.Fatalf("provider-set = %q", set.Text)
	}

	options, err := h.inForce(h.current.choice, effort)

	if err != nil || string(options) != `{"effort":"low"}` {
		t.Fatalf("options in force = %s, err = %v", options, err)
	}

	worker, err := h.agent(nil, h.current.choice, options, h.current.tools, nil)

	if err != nil || worker.MaxTurns != 7 || string(worker.Options) != `{"effort":"low"}` {
		t.Fatalf("worker = %+v, err = %v", worker, err)
	}

	if provider := call(t, h, "system-set", `{"change":"default_provider=claude"}`); !strings.Contains(provider.Text, "from the next start of rumbo") {
		t.Fatalf("system-set default_provider = %q", provider.Text)
	}
}

// Continuing a session from the command line starts with its provider,
// unless -m chooses another for that start. A new session starts with the
// provider's own model.
func TestContinuingASessionUsesItsProvider(t *testing.T) {
	h := testHost(t)
	withClaude(t, h.database, "")
	setSettings(h.database, []string{"default_provider=first"})

	_, root, err := workingProject()

	if err != nil {
		t.Fatal(err)
	}

	// A new session takes the default provider's model.
	fresh, err := newHost(h.database, nil)

	if err != nil || fresh.current.provider != "first" || fresh.current.model != "small" {
		t.Fatalf("a new session: %+v, err = %v", fresh, err)
	}

	fresh.close()

	session, _ := h.database.CreateSession(root, root, "continued")
	h.database.SetSessionModel(session.ID, "claude", "large")

	continued, err := newHost(h.database, []string{"-s", session.ID})

	if err != nil || continued.current.provider != "claude" || continued.current.model != "large" {
		t.Fatalf("continued: %+v, err = %v", continued, err)
	}

	continued.close()

	// What the command line chooses is the session's from then on, and an
	// option is checked against what the provider describes.
	chosen, err := newHost(h.database, []string{"-s", session.ID, "-m", "org/huge", "-o", "effort=high"})

	if err != nil || chosen.current.provider != "claude" || chosen.current.model != "org/huge" {
		t.Fatalf("with -m: %+v, err = %v", chosen, err)
	}

	chosen.close()

	if stored, _ := h.database.Session(session.ID); stored.Provider != "claude" || stored.Model != "org/huge" || string(stored.Options) != `{"effort":"high"}` {
		t.Fatalf("the session keeps %s/%s %s", stored.Provider, stored.Model, stored.Options)
	}

	if _, err := newHost(h.database, []string{"-s", session.ID, "-o", "effrt=high"}); err == nil || !strings.Contains(err.Error(), `claude has no option "effrt"`) {
		t.Fatalf("-o with an option the provider does not describe: err = %v", err)
	}

	// Another provider starts without the options chosen for the one
	// before, and with its own model.
	switched, err := newHost(h.database, []string{"-s", session.ID, "-p", "first"})

	if err != nil {
		t.Fatal(err)
	}

	if switched.current.model != "small" || switched.current.options != nil {
		t.Fatalf("with -p: %+v", switched.current.choice)
	}

	switched.close()

	// One without a model of its own starts all the same, for the front end
	// to choose one; it is a run that needs one.
	modelless, err := newHost(h.database, []string{"-p", "second"})

	if err != nil || modelless.current.model != "" {
		t.Fatalf("a provider without a model: %+v, err = %v", modelless, err)
	}

	if _, err := modelless.accept(protocol.RunParams{Objective: "go"}, nil); err == nil || !strings.Contains(err.Error(), "no model chosen for second: choose one with use") {
		t.Fatalf("a run without a model: err = %v", err)
	}

	modelless.close()

	if _, err := newHost(h.database, []string{"-p", "second", "fix it"}); err == nil || !strings.Contains(err.Error(), `run "rumbo provider set second model=<model>"`) {
		t.Fatalf("a task for a provider without a model: err = %v", err)
	}

	h.database.SetSessionModel(session.ID, "gone", "x")

	if _, err := newHost(h.database, []string{"-s", session.ID}); err == nil || !strings.Contains(err.Error(), `uses the provider "gone"`) {
		t.Fatalf("a session whose provider was removed: err = %v", err)
	}
}

// Status says how much of the session's input the provider read from its
// cache, which is what a stable start of each request saves.
func TestStatusSaysHowMuchCameFromTheCache(t *testing.T) {
	h := testHost(t)
	session, _ := h.database.CreateSession("/repo", "/repo", "cached")
	h.current.session = &session

	reply := protocol.Message{Role: protocol.RoleAssistant, Content: []protocol.Block{{Type: protocol.BlockText, Text: "ok"}}}

	// The first request writes the cache, and the second reads most of it.
	h.database.Append(session.ID, transcript.KindUser, transcript.Text{Text: "a task"})
	h.database.Append(session.ID, transcript.KindAssistant, transcript.Assistant{Message: reply, Usage: protocol.Usage{Input: 1000, CacheWrite: 1000}})
	h.database.Append(session.ID, transcript.KindUser, transcript.Text{Text: "another"})
	h.database.Append(session.ID, transcript.KindAssistant, transcript.Assistant{Message: reply, Usage: protocol.Usage{Input: 100, CacheWrite: 100, CacheRead: 1800}})

	// 1800 read from the cache out of 4000 input tokens in all.
	if status := call(t, h, "status", ""); !strings.Contains(status.Text, "from the cache: 45% of all input") {
		t.Fatalf("status = %q", status.Text)
	}
}

// A front end asks what a session holds: the current one, or another of the
// project; a new session holds nothing yet.
func TestHistoryTellsTheSessionAsked(t *testing.T) {
	h := testHost(t)

	if told, err := h.history(protocol.HistoryParams{}); err != nil || told.Session != "" || len(told.Events) != 0 {
		t.Fatalf("history of a new session = %+v, err = %v", told, err)
	}

	earlier, _ := h.database.CreateSession("/repo", "/repo", "earlier")
	h.database.Append(earlier.ID, transcript.KindUser, transcript.Text{Text: "a task"})
	h.database.Append(earlier.ID, transcript.KindNote, transcript.Note{Type: transcript.NoteDone})

	told, err := h.history(protocol.HistoryParams{Session: earlier.ID})

	if err != nil || told.Session != earlier.ID || len(told.Events) != 2 {
		t.Fatalf("history = %+v, err = %v", told, err)
	}

	if first := told.Events[0]; first.Type != protocol.EventObjective || first.Text != "a task" || first.Session != earlier.ID {
		t.Fatalf("first event = %+v", first)
	}

	elsewhere, _ := h.database.CreateSession("/other", "/other", "elsewhere")

	if _, err := h.history(protocol.HistoryParams{Session: elsewhere.ID}); err == nil || !strings.Contains(err.Error(), "belongs to /other") {
		t.Fatalf("history of another project's session: err = %v", err)
	}
}

// What the person asked in the project's sessions comes back newest first:
// objectives and steers, and nothing the model said or another project
// holds.
func TestRecallTellsWhatThePersonAsked(t *testing.T) {
	h := testHost(t)

	if recalled, err := h.recall(); err != nil || recalled.Asked == nil || len(recalled.Asked) != 0 {
		t.Fatalf("recall with no sessions = %#v, err = %v", recalled, err)
	}

	here, _ := h.database.CreateSession("/repo", "/repo", "here")
	elsewhere, _ := h.database.CreateSession("/other", "/other", "elsewhere")

	for _, appended := range []struct {
		session string
		kind    string
		payload any
	}{
		{here.ID, transcript.KindUser, transcript.Text{Text: "fix the parser"}},
		{here.ID, transcript.KindAssistant, transcript.Assistant{}},
		{here.ID, transcript.KindSteer, transcript.Text{Text: "and its tests"}},
		{elsewhere.ID, transcript.KindUser, transcript.Text{Text: "not here"}},
	} {
		time.Sleep(2 * time.Millisecond)
		h.database.Append(appended.session, appended.kind, appended.payload)
	}

	recalled, err := h.recall()

	if err != nil || !slices.Equal(recalled.Asked, []string{"and its tests", "fix the parser"}) {
		t.Fatalf("recall = %q, err = %v", recalled.Asked, err)
	}
}

// A branch is a new session that starts from the current one, as it is or
// as it was before one of the person's messages, and leaves it as it is.
func TestBranchesStartFromAPointOfASession(t *testing.T) {
	h := testHost(t)

	if err := refused(t, h, "branch", ""); !strings.Contains(err, "no session to branch") {
		t.Fatalf("branching before any session: %v", err)
	}

	call(t, h, "use", `{"provider":"second","model":"large"}`)
	source, _ := h.made(h.current, "fix the parser")

	for _, entry := range []struct {
		kind    string
		payload any
	}{
		{transcript.KindSystem, transcript.System{Text: "prompt"}},
		{transcript.KindUser, transcript.Text{Text: "fix the parser"}},
		{transcript.KindNote, transcript.Note{Type: transcript.NoteDone}},
		{transcript.KindContext, transcript.Context{Sections: []transcript.Section{{Name: "environment", Text: "here"}}}},
		{transcript.KindUser, transcript.Text{Text: "now document it\nin the README"}},
		{transcript.KindNote, transcript.Note{Type: transcript.NoteDone}},
	} {
		h.database.Append(source.ID, entry.kind, entry.payload)
	}

	points := call(t, h, "branch-points", "")

	if len(points.Values) != 2 || points.Values[0].Value != "1" || points.Values[1].Label != "now document it" {
		t.Fatalf("branch-points = %+v", points.Values)
	}

	// A run works in the session: a branch can still start before one of
	// its messages, not from what the run is recording.
	h.busy[h.current] = busyRun

	if err := refused(t, h, "branch", ""); !strings.Contains(err, "is busy with a run") {
		t.Fatalf("branching from now during a run: %v", err)
	}

	for _, point := range []string{"0", "3", "two"} {
		refused(t, h, "branch", `{"point":"`+point+`"}`)
	}

	branched := call(t, h, "branch", `{"point":"2"}`)

	for _, said := range []string{"before your message 2", "the files are as they are now", "a run goes on in session " + source.ID, "your message was:\nnow document it\nin the README"} {
		if !strings.Contains(branched.Text, said) {
			t.Fatalf("branch = %q, missing %q", branched.Text, said)
		}
	}

	branch := h.current.session

	if branched.State.Session != branch.ID || branch.Parent != source.ID || branch.ParentSeq != 4 || h.current.provider != "second" || h.current.model != "large" {
		t.Fatalf("the branch is %+v, choosing %+v", branch, h.current.choice)
	}

	// Its requests are those the session it came from made at that point.
	copied, _ := h.database.Entries(branch.ID)
	original, _ := h.database.Entries(source.ID)

	if len(copied) != 4 || len(original) != 6 {
		t.Fatalf("the branch has %d entries and the session %d", len(copied), len(original))
	}

	fromBranch, _ := transcript.Build(copied)
	fromSource, _ := transcript.Build(original[:4])

	if fromBranch.Objective != "fix the parser" || len(fromBranch.Messages) != len(fromSource.Messages) {
		t.Fatalf("the branch builds %+v", fromBranch)
	}

	if stored, _ := h.database.Session(branch.ID); stored.Provider != "second" || stored.Model != "large" {
		t.Fatalf("the branch keeps %s/%s", stored.Provider, stored.Model)
	}

	// A branch of the branch, as it is now.
	if again := call(t, h, "branch", ""); !strings.Contains(again.Text, "a branch of "+branch.ID+" as it is") {
		t.Fatalf("branch from now = %q", again.Text)
	}

	if whole, _ := h.database.Entries(h.current.session.ID); len(whole) != 4 {
		t.Fatalf("the branch from now has %d entries", len(whole))
	}
}
