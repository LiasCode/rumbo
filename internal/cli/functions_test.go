package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LiasCode/rumbo/internal/extension"
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
		dir:      "/repo",
		tools:    tools,
		settings: config{DefaultProvider: "first"},
		choice:   choice{provider: "first", model: "small"},
		pools:    map[string]*provider.Pool{},

		extensions: extension.NewSet(func(string) io.Writer { return io.Discard }, tools.Clip),
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

// restarted is the host a later start of rumbo makes to continue a session,
// as rumbo -s does.
func restarted(t *testing.T, database store.Store, session string) *host {
	t.Helper()

	continued, err := newHost(database, []string{"-s", session})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(continued.close)

	return continued
}

// The host's functions act on its one session. What listed other sessions,
// or left this one for another, is no function of the host: another
// session is another rumbo.
func TestHostFunctionsActOnItsSession(t *testing.T) {
	h := testHost(t)

	if status := call(t, h, "status", ""); !strings.Contains(status.Text, "session: none yet") || status.State.Dir != "/repo" || status.State.Session != "" {
		t.Fatalf("status of a new session = %q, %+v", status.Text, status.State)
	}

	session, err := h.made("older work")

	if err != nil {
		t.Fatal(err)
	}

	if status := call(t, h, "status", ""); !strings.Contains(status.Text, "session: "+session.ID) || !strings.Contains(status.Text, "context: 0 tokens") || status.State.Session != session.ID {
		t.Fatalf("status = %q, %+v", status.Text, status.State)
	}

	for _, name := range []string{"sessions", "projects", "open", "new", "delete", "prune"} {
		if err := refused(t, h, name, `{"session":"`+session.ID+`"}`); !strings.Contains(err, "the host has no function") {
			t.Fatalf("%s: err = %v", name, err)
		}
	}

	// The providers are the built-in ones and those registered, by name.
	if providers := call(t, h, "providers", ""); shownValues(providers.Values) != "anthropic: | chatgpt: | first: | openai: | second:" {
		t.Fatalf("providers = %+v", providers.Values)
	}

	// Changing provider brings the options that provider's configuration
	// gives, not the ones in force for the one before.
	used := call(t, h, "use", `{"provider":"second","model":"large"}`)

	if options, _ := h.inForce(h.choice, depth); used.State.Provider != "second" || used.State.Model != "large" || string(options) != `{"depth":2}` {
		t.Fatalf("after use: %+v, options %s", used.State, options)
	}

	for name, params := range map[string]string{"use": `{"provider":"missing","model":"x"}`, "rename": `{}`, "nope": ``} {
		refused(t, h, name, params)
	}
}

// working makes the host's session one where a run works.
func working(t *testing.T, h *host) store.Session {
	t.Helper()

	session, _ := h.database.CreateSession("/repo", "in progress")
	h.database.Append(session.ID, "user", map[string]string{"text": "the objective"})

	h.session = &session
	h.busy = busyRun

	if err := h.hold(); err != nil {
		t.Fatal(err)
	}

	return session
}

// The session has one run at a time, and what would change what the run
// works on waits for it; the rest is answered while it works.
func TestTheSessionHasOneRunAtATime(t *testing.T) {
	h := testHost(t)
	session := working(t, h)

	// The run has no ending yet because it is still going, not because it
	// was interrupted.
	if status := call(t, h, "status", ""); !strings.Contains(status.Text, "a run is in progress") || strings.Contains(status.Text, "resumed") {
		t.Fatalf("status during a run = %q", status.Text)
	}

	if err := refused(t, h, "compact", ""); !strings.Contains(err, "busy with a run") {
		t.Fatalf("compacting the session that works: err = %v", err)
	}

	// The session's provider changes for the runs that follow it.
	if used := call(t, h, "use", `{"provider":"second","model":"large"}`); !strings.Contains(used.Text, "keeps the provider it started with") || h.provider != "second" {
		t.Fatalf("use during a run = %q", used.Text)
	}

	if _, err := h.accept(protocol.RunParams{Objective: "more"}, nil); err == nil || !strings.Contains(err.Error(), "session "+session.ID+" is busy with a run") {
		t.Fatalf("a second run: err = %v", err)
	}

	// The host holds its session from the first work in it until it ends,
	// so no other rumbo process works in it between two runs.
	h.finish()

	if _, err := h.accept(protocol.RunParams{Objective: "more"}, nil); err != nil {
		t.Fatalf("a run once the one before ended: %v", err)
	}

	h.finish()

	if _, err := h.database.LockSession(session.ID); err == nil {
		t.Fatal("the session is not held by its host between runs")
	}

	h.close()

	if unlock, err := h.database.LockSession(session.ID); err != nil {
		t.Fatal("the session is still held after its host ended")
	} else {
		unlock()
	}
}

// A session keeps its provider from one start of rumbo to the next: the one
// it was made with, or the one use chose for it.
func TestSessionsKeepTheirProviderAcrossStarts(t *testing.T) {
	h := testHost(t)

	setSettings(h.database, []string{"default_provider=first"})

	made, err := h.made("first task")

	if err != nil {
		t.Fatal(err)
	}

	if stored, _ := h.database.Session(made.ID); stored.Provider != "first" || stored.Model != "small" {
		t.Fatalf("a new session stored %s/%s", stored.Provider, stored.Model)
	}

	call(t, h, "use", `{"provider":"second","model":"large"}`)
	older, _ := h.database.CreateSession("/repo", "from before providers were stored")
	h.close()

	// A later start, whose settings say first/small, finds each session with
	// its own provider; one without a stored provider takes the system's.
	continued := restarted(t, h.database, made.ID)

	if options, _ := continued.inForce(continued.choice, depth); continued.provider != "second" || continued.model != "large" || string(options) != `{"depth":2}` || continued.dir != "/repo" {
		t.Fatalf("after a restart: %+v in %s, options %s", continued.choice, continued.dir, options)
	}

	if unstored := restarted(t, h.database, older.ID); unstored.provider != "first" || unstored.model != "small" {
		t.Fatalf("a session with no stored provider: %+v", unstored.choice)
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

	if options, _ := h.inForce(h.choice, effort); string(options) != `{"effort":"low"}` {
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

	made, err := h.made("first task")

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

	later := restarted(t, h.database, made.ID)

	for _, said := range []string{
		`options chosen in the session: {"effort":"max"}`,
		`configuration of claude, under them: {"base_url":"http://127.0.0.1:9","effort":"high"}`,
	} {
		if status := call(t, later, "status", ""); !strings.Contains(status.Text, said) {
			t.Fatalf("status after a restart = %q, missing %q", status.Text, said)
		}
	}

	// An empty value gives one back to the provider's, and only one chosen.
	if back := call(t, later, "session-set", `{"change":"effort="}`); !strings.HasPrefix(back.Text, "effort: given back to claude's") || later.options != nil {
		t.Fatalf("session-set effort= = %q, options %s", back.Text, later.options)
	}

	if err := refused(t, later, "session-set", `{"change":"effort="}`); !strings.Contains(err, "not chosen in the session") {
		t.Fatalf("giving back what is not chosen: %s", err)
	}

	// Another provider does not get them.
	call(t, later, "session-set", `{"change":"effort=low"}`)
	used := call(t, later, "use", `{"provider":"first","model":"small"}`)

	if !strings.Contains(used.Text, "were for another provider and are dropped") || later.options != nil {
		t.Fatalf("use = %q, options %s", used.Text, later.options)
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

	if string(h.options) != `{"effort":"high","note":"hey"}` {
		t.Fatalf("the session kept %s", h.options)
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

	options, err := h.inForce(h.choice, effort)

	if err != nil || string(options) != `{"effort":"low"}` {
		t.Fatalf("options in force = %s, err = %v", options, err)
	}

	worker, err := h.agent(context.Background(), nil, h.choice, options, nil)

	if err != nil || worker.MaxTurns != 7 || string(worker.Options) != `{"effort":"low"}` {
		t.Fatalf("worker = %+v, err = %v", worker, err)
	}

	if provider := call(t, h, "system-set", `{"change":"default_provider=claude"}`); !strings.Contains(provider.Text, "from the next start of rumbo") {
		t.Fatalf("system-set default_provider = %q", provider.Text)
	}
}

// A field written for a provider in config.yaml that the provider has no
// setting of, such as a misspelled one, is refused once the provider
// starts, saying where it is and how to take it out, rather than ignored.
// A provider that says nothing of its configuration is taken as it is.
func TestAProviderIsRefusedAFieldItDoesNotHave(t *testing.T) {
	h := testHost(t)
	withClaude(t, h.database, `{"efort":"high"}`)
	call(t, h, "use", `{"provider":"claude","model":"claude-x"}`)

	_, err := h.describe(context.Background())

	if err == nil || !strings.Contains(err.Error(), "config.yaml, under providers, claude") || !strings.Contains(err.Error(), `"efort"`) || !strings.Contains(err.Error(), `"rumbo provider set claude efort="`) {
		t.Fatalf("describe with a field claude does not have: %v", err)
	}

	if err := refused(t, h, "session-values", ""); !strings.Contains(err, `"efort"`) {
		t.Fatalf("session-values with a field claude does not have: %v", err)
	}

	// Taking it out lets it work again.
	call(t, h, "provider-set", `{"provider":"claude","change":"efort="}`)

	if _, err := h.describe(context.Background()); err != nil {
		t.Fatalf("describe once it is taken out: %v", err)
	}

	quiet := store.Program{Name: "quiet", Command: []string{selfPlaceholder, "provide", "two-options"}, Config: json.RawMessage(`{"anything":1}`)}
	h.database.PutProgram(store.KindProvider, quiet)
	call(t, h, "use", `{"provider":"quiet","model":"any"}`)

	if _, err := h.describe(context.Background()); err != nil {
		t.Fatalf("describe a provider that says nothing of its configuration: %v", err)
	}
}

// rumbo looks no further than the directory it is started in: a session
// started above it is not continued from it, at the top of its git
// repository either, and the session it starts is that directory's.
func TestASessionBelongsToTheDirectoryRumboIsStartedIn(t *testing.T) {
	h := testHost(t)
	setSettings(h.database, []string{"default_provider=first"})

	repository := t.TempDir()

	if output, err := exec.Command("git", "init", "--quiet", repository).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s", output)
	}

	inside := filepath.Join(repository, "pkg")

	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(inside)
	h.database.CreateSession(repository, "above")

	if _, err := newHost(h.database, []string{"-c"}); err == nil || !strings.Contains(err.Error(), "no session to continue in "+inside) {
		t.Fatalf("continuing where no session was started: err = %v", err)
	}

	started, err := newHost(h.database, nil)

	if err != nil {
		t.Fatal(err)
	}

	defer started.close()

	if started.dir != inside || started.info.Dir != inside {
		t.Fatalf("started in %s: dir %s, told %s", inside, started.dir, started.info.Dir)
	}
}

// Continuing a session from the command line starts with its provider,
// unless -m chooses another for that start. A new session starts with the
// provider's own model.
func TestContinuingASessionUsesItsProvider(t *testing.T) {
	h := testHost(t)
	withClaude(t, h.database, "")
	setSettings(h.database, []string{"default_provider=first"})

	root, err := os.Getwd()

	if err != nil {
		t.Fatal(err)
	}

	// A new session takes the default provider's model.
	fresh, err := newHost(h.database, nil)

	if err != nil || fresh.provider != "first" || fresh.model != "small" {
		t.Fatalf("a new session: %+v, err = %v", fresh, err)
	}

	fresh.close()

	session, _ := h.database.CreateSession(root, "continued")
	h.database.SetSessionModel(session.ID, "claude", "large")

	continued, err := newHost(h.database, []string{"-s", session.ID})

	if err != nil || continued.provider != "claude" || continued.model != "large" {
		t.Fatalf("continued: %+v, err = %v", continued, err)
	}

	continued.close()

	// What the command line chooses is the session's from then on, and an
	// option is checked against what the provider describes.
	chosen, err := newHost(h.database, []string{"-s", session.ID, "-m", "org/huge", "-o", "effort=high"})

	if err != nil || chosen.provider != "claude" || chosen.model != "org/huge" {
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

	if switched.model != "small" || switched.options != nil {
		t.Fatalf("with -p: %+v", switched.choice)
	}

	switched.close()

	// One without a model of its own starts all the same, for the front end
	// to choose one; it is a run that needs one.
	modelless, err := newHost(h.database, []string{"-p", "second"})

	if err != nil || modelless.model != "" {
		t.Fatalf("a provider without a model: %+v, err = %v", modelless, err)
	}

	if _, err := modelless.accept(protocol.RunParams{Objective: "go"}, nil); err == nil || !strings.Contains(err.Error(), "no model chosen for second: choose one for this session") {
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
	session, _ := h.database.CreateSession("/repo", "cached")
	h.session = &session

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

// A front end asks what the host's session holds; a new session holds
// nothing yet.
func TestHistoryTellsWhatTheSessionHolds(t *testing.T) {
	h := testHost(t)

	if told, err := h.history(); err != nil || told.Session != "" || len(told.Events) != 0 {
		t.Fatalf("history of a new session = %+v, err = %v", told, err)
	}

	earlier, _ := h.database.CreateSession("/repo", "earlier")
	h.database.Append(earlier.ID, transcript.KindUser, transcript.Text{Text: "a task"})
	h.database.Append(earlier.ID, transcript.KindNote, transcript.Note{Type: transcript.NoteDone})
	h.session = &earlier

	told, err := h.history()

	if err != nil || told.Session != earlier.ID || len(told.Events) != 2 {
		t.Fatalf("history = %+v, err = %v", told, err)
	}

	if first := told.Events[0]; first.Type != protocol.EventObjective || first.Text != "a task" || first.Session != earlier.ID {
		t.Fatalf("first event = %+v", first)
	}
}

// What the person asked in the sessions of the host's directory comes back
// newest first: objectives and steers, and nothing the model said or the
// sessions of another directory hold.
func TestRecallTellsWhatThePersonAsked(t *testing.T) {
	h := testHost(t)

	if recalled, err := h.recall(); err != nil || recalled.Asked == nil || len(recalled.Asked) != 0 {
		t.Fatalf("recall with no sessions = %#v, err = %v", recalled, err)
	}

	here, _ := h.database.CreateSession("/repo", "here")
	elsewhere, _ := h.database.CreateSession("/other", "elsewhere")

	for _, appended := range []struct {
		session string
		kind    string
		payload any
	}{
		{here.ID, transcript.KindUser, transcript.Text{Text: "fix the parser"}},
		{here.ID, transcript.KindAssistant, transcript.Assistant{}},
		{here.ID, transcript.KindSteer, transcript.Text{Text: "and its tests"}},
		{elsewhere.ID, transcript.KindUser, transcript.Text{Text: "not here"}},
		// The same words with an image are the same text to bring back.
		{here.ID, transcript.KindUser, transcript.Text{Text: "fix the parser", Images: []protocol.Image{{MediaType: "image/png", Data: []byte("png")}}}},
	} {
		time.Sleep(2 * time.Millisecond)
		h.database.Append(appended.session, appended.kind, appended.payload)
	}

	recalled, err := h.recall()

	if err != nil || !slices.Equal(recalled.Asked, []string{"fix the parser", "and its tests"}) {
		t.Fatalf("recall = %q, err = %v", recalled.Asked, err)
	}
}

// A branch is a new session that starts from the host's, as it is or as it
// was before one of the person's messages, and leaves it as it is. The host
// stays in its session, and says which the branch is, for another rumbo.
func TestBranchesStartFromAPointOfASession(t *testing.T) {
	h := testHost(t)

	if err := refused(t, h, "branch", ""); !strings.Contains(err, "no session to branch") {
		t.Fatalf("branching before any session: %v", err)
	}

	call(t, h, "use", `{"provider":"second","model":"large"}`)
	source, _ := h.made("fix the parser")

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
	h.busy = busyRun

	if err := refused(t, h, "branch", ""); !strings.Contains(err, "is busy with a run") {
		t.Fatalf("branching from now during a run: %v", err)
	}

	// What is refused says how many messages there are, naming no
	// function.
	for _, point := range []string{"0", "3", "two"} {
		if err := refused(t, h, "branch", `{"point":"`+point+`"}`); err != "there is no message "+point+" of yours in this session, which has 2" {
			t.Fatalf("branching before message %s: %v", point, err)
		}
	}

	branched := call(t, h, "branch", `{"point":"2"}`)

	if len(branched.Values) != 1 || branched.Values[0].Label != "Branch fix the parser" {
		t.Fatalf("branch answered with %+v", branched.Values)
	}

	branch, err := h.database.Session(branched.Values[0].Value)

	if err != nil {
		t.Fatal(err)
	}

	for _, said := range []string{"session " + branch.ID + " is a branch of this one from before your message 2", "the files are as they are now", "rumbo -s " + branch.ID, "your message was:\nnow document it\nin the README"} {
		if !strings.Contains(branched.Text, said) {
			t.Fatalf("branch = %q, missing %q", branched.Text, said)
		}
	}

	// The host stays in its session, which goes on working.
	if branched.State.Session != source.ID || h.session.ID != source.ID || h.busy != busyRun {
		t.Fatalf("after a branch the host is in %+v, busy with %q", branched.State, h.busy)
	}

	if branch.Parent != source.ID || branch.ParentSeq != 4 || branch.Dir != "/repo" || branch.Provider != "second" || branch.Model != "large" {
		t.Fatalf("the branch is %+v", branch)
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

	// Another rumbo goes on in the branch, with what the session used.
	h.close()

	other := restarted(t, h.database, branch.ID)

	if other.session.ID != branch.ID || other.provider != "second" || other.model != "large" {
		t.Fatalf("the rumbo that continues the branch is in %+v, choosing %+v", other.session, other.choice)
	}

	// A branch of the branch, as it is now.
	again := call(t, other, "branch", "")

	if !strings.Contains(again.Text, "is a branch of this one as it is") || len(again.Values) != 1 {
		t.Fatalf("branch from now = %+v", again)
	}

	if whole, _ := other.database.Entries(again.Values[0].Value); len(whole) != 4 {
		t.Fatalf("the branch from now has %d entries", len(whole))
	}
}
