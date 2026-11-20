package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LiasCode/rumbo/internal/agent"
	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/protocol"
)

// testStore is a store in files of the test's own, with nothing chosen.
func testStore(t *testing.T) store.Store {
	t.Helper()

	return store.Open(filepath.Join(t.TempDir(), configFile), t.TempDir())
}

func TestSettingsAreCheckedAndResolvedInOnePlace(t *testing.T) {
	database := testStore(t)

	for value, want := range map[string]agent.Compaction{
		"":    {},
		"0%":  {Never: true},
		"70%": {Percent: 70},
	} {
		if got, err := parseCompactAt(value); err != nil || got != want {
			t.Errorf("compact_at %q = %+v, %v; want %+v", value, got, err, want)
		}
	}

	// A number of tokens, which an earlier version took, says how to write
	// a share instead.
	if _, err := parseCompactAt("90000"); err == nil || !strings.Contains(err.Error(), "rumbo system set compact_at 80%") {
		t.Fatalf("compact_at in tokens: err = %v", err)
	}

	for key, value := range map[string]string{
		"compact_at":       "150%",
		"default_provider": "nope",
		"default_model":    " ",
		"max_turns":        "many",
		"frontend":         "missing",
		"no_such_setting":  "1",
	} {
		if err := setSetting(database, key, value); err == nil {
			t.Errorf("%s = %s was accepted", key, value)
		}
	}

	// A built-in provider is there without being registered.
	if err := setSetting(database, "default_provider", "anthropic"); err != nil {
		t.Fatal(err)
	}

	setSetting(database, "default_model", "org/big-model")

	if turns, _ := effectiveSetting(database, "max_turns"); turns.Value != "150" || turns.Source != sourceDefault {
		t.Fatalf("max_turns = %+v", turns)
	}

	listed, _ := effectiveSettings(database)
	text := describeSettings(listed)

	for _, line := range []string{
		"default_provider     anthropic  (stored)",
		"default_model        org/big-model  (stored)",
		"frontend             (not set)",
		"max_turns            150  (default)",
		"compact_at           80%  (default)",
	} {
		if !strings.Contains(text, line) {
			t.Errorf("missing %q in:\n%s", line, text)
		}
	}

	if err := unsetSetting(database, "default_model"); err != nil {
		t.Fatal(err)
	}

	if err := unsetSetting(database, "default_model"); err == nil {
		t.Fatal("removing a value that is not stored reported nothing")
	}

	setSetting(database, "default_model", "big")

	loaded, err := loadConfig(database, "plain")

	if err != nil || loaded.DefaultProvider != "anthropic" || loaded.DefaultModel != "big" || loaded.Frontend != "plain" {
		t.Fatalf("loaded = %+v, err = %v", loaded, err)
	}

	limits, err := loadLimits(database)

	if err != nil || limits.MaxTurns != 150 || limits.Compaction != (agent.Compaction{Percent: 80}) || limits.MaxTokens != 0 {
		t.Fatalf("limits = %+v, err = %v", limits, err)
	}

	// One that is malformed in the file, as a person may write it, stops
	// rumbo before it starts.
	database.SetSetting("compact_at", "90000")

	if _, err := loadConfig(database, "plain"); err == nil || !strings.Contains(err.Error(), "compact_at") {
		t.Fatalf("a malformed compact_at: err = %v", err)
	}

	// So does a name that is no setting, which is how a misspelled one shows.
	database.DeleteSetting("compact_at")
	database.SetSetting("max_turn", "40")

	if _, err := loadConfig(database, "plain"); err == nil || !strings.Contains(err.Error(), `config.yaml, under system: there is no system setting "max_turn"`) {
		t.Fatalf("a misspelled setting: err = %v", err)
	}
}

// A run gets, with every request, the fields of its provider's
// configuration that the provider describes as options, and the session's
// over them.
func TestOptionsComeFromTheProvidersConfiguration(t *testing.T) {
	described := []protocol.Option{{Name: "effort"}, {Name: "web"}, {Name: "depth"}}
	config := json.RawMessage(`{"base_url":"http://mine","effort":"low","web":false}`)

	for session, want := range map[string]string{
		``:                            `{"effort":"low","web":false}`,
		`{"effort":"high","depth":5}`: `{"depth":5,"effort":"high","web":false}`,
	} {
		if inForce, err := optionsInForce(config, described, json.RawMessage(session)); err != nil || string(inForce) != want {
			t.Errorf("with %q in the session: %s, err = %v; want %s", session, inForce, err, want)
		}
	}

	// Nothing chosen is no options at all, so the provider's defaults apply.
	if none, err := optionsInForce(json.RawMessage(`{"base_url":"http://mine"}`), described, nil); err != nil || none != nil {
		t.Fatalf("with nothing chosen: %s, err = %v", none, err)
	}

	// A provider that describes none gets what the session chose.
	if passed, _ := optionsInForce(config, nil, json.RawMessage(`{"effort":"high"}`)); string(passed) != `{"effort":"high"}` {
		t.Fatalf("without a description: %s", passed)
	}

	if _, err := optionsInForce(json.RawMessage(`"fast"`), described, nil); err == nil {
		t.Fatal("a configuration that is no object was accepted")
	}
}

// A provider is started when it is added, which is the test that it works,
// and kept with what the user gave it and nothing else: its defaults are
// its own, and it says them when asked.
func TestAProviderIsStartedWhenAdded(t *testing.T) {
	database := testStore(t)

	if err := registerProvider(database, "local", []string{selfPlaceholder, "provide", "openai"}, json.RawMessage(`{"base_url":"http://localhost:11434/v1"}`), io.Discard); err != nil {
		t.Fatal(err)
	}

	program, _ := database.Program(store.KindProvider, "local")

	if string(program.Config) != `{"base_url":"http://localhost:11434/v1"}` {
		t.Fatalf("registered with %s", program.Config)
	}

	shown, err := inspectProvider(database, "local", io.Discard)

	for _, line := range []string{
		"name: local\n",
		`configuration: {"base_url":"http://localhost:11434/v1"}` + "\n",
		`in force, with its defaults: {"base_url":"http://localhost:11434/v1","api_key_env":"OPENAI_API_KEY","context_window":0,"effort":"","api":"chat"}` + "\n",
		"option effort: ",
	} {
		if err != nil || !strings.Contains(shown, line) {
			t.Fatalf("show has no %q, err = %v:\n%s", line, err, shown)
		}
	}

	// One that does not start is not registered.
	if err := registerProvider(database, "broken", []string{"/no/such/program"}, nil, io.Discard); err == nil || !strings.Contains(err.Error(), "was not registered") {
		t.Fatalf("a provider that does not start: err = %v", err)
	}

	if _, err := database.Program(store.KindProvider, "broken"); err == nil {
		t.Fatal("a provider that does not start was registered")
	}
}

// The built-in programs are there without being registered. What the user
// chooses for one is all that is kept of it, over what it is built in with.
func TestBuiltinsAreThereWithoutBeingRegistered(t *testing.T) {
	database := testStore(t)

	// The openai one speaks the Responses API, which OpenAI's own models
	// need; the others need nothing.
	for name, want := range map[string]string{"openai": `{"api":"responses"}`, "anthropic": "", "chatgpt": ""} {
		program, err := findProgram(database, store.KindProvider, name)

		if err != nil || string(program.Config) != want || strings.Join(program.Command, " ") != "{self} provide "+name {
			t.Fatalf("%s is %+v, err %v; want the configuration %q", name, program, err, want)
		}
	}

	if _, err := database.Program(store.KindProvider, "openai"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a built-in nothing was chosen for is registered: err = %v", err)
	}

	listed, err := listPrograms(database, store.KindFrontend)

	if err != nil || len(listed) != 2 || listed[0].Name != "plain" || listed[1].Name != "tui" {
		t.Fatalf("front ends = %+v, err = %v", listed, err)
	}

	configured, err := configureProgram(database, store.KindProvider, "openai", []string{"effort=high", "api=chat"})

	if err != nil || string(configured.Config) != `{"api":"chat","effort":"high"}` {
		t.Fatalf("configured = %+v, err = %v", configured, err)
	}

	if kept, _ := database.Program(store.KindProvider, "openai"); len(kept.Command) != 0 || string(kept.Config) != `{"api":"chat","effort":"high"}` {
		t.Fatalf("kept of openai: %+v", kept)
	}

	// A field given back goes to what the built-in has, and nothing is kept
	// of a built-in once nothing is chosen for it.
	if back, _ := configureProgram(database, store.KindProvider, "openai", []string{"api="}); string(back.Config) != `{"api":"responses","effort":"high"}` {
		t.Fatalf("after giving api back: %s", back.Config)
	}

	configureProgram(database, store.KindProvider, "openai", []string{"effort="})

	if _, err := database.Program(store.KindProvider, "openai"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a built-in with nothing chosen is still registered: err = %v", err)
	}

	// It cannot be removed; what was chosen for it can.
	if _, err := removeProgram(database, store.KindProvider, "openai"); err == nil || !strings.Contains(err.Error(), "is built in") {
		t.Fatalf("removing a built-in: err = %v", err)
	}

	configureProgram(database, store.KindProvider, "openai", []string{"effort=low"})

	if removed, err := removeProgram(database, store.KindProvider, "openai"); err != nil || !strings.Contains(removed, "as rumbo has it again") {
		t.Fatalf("removing what was chosen for a built-in: %q, err = %v", removed, err)
	}

	// One registered under a built-in's name takes its place.
	registerProgram(database, store.KindProvider, "openai", []string{"my-openai"}, nil)

	if mine, _ := findProgram(database, store.KindProvider, "openai"); strings.Join(mine.Command, " ") != "my-openai" || mine.Config != nil {
		t.Fatalf("openai registered by the user is %+v", mine)
	}

	// A name with only a configuration, and nothing built in, cannot start.
	database.PutProgram(store.KindProvider, store.Program{Name: "orphan", Config: json.RawMessage(`{"a":1}`)})

	if _, err := findProgram(database, store.KindProvider, "orphan"); err == nil || !strings.Contains(err.Error(), "has no command in config.yaml") {
		t.Fatalf("a provider without a command: err = %v", err)
	}
}

func TestConfigureProgramChangesOnlyTheFieldsGiven(t *testing.T) {
	database := testStore(t)
	registerProgram(database, store.KindProvider, "mine", []string{"run"}, []byte(`{"base_url":"http://a","effort":"low"}`))

	program, err := configureProgram(database, store.KindProvider, "mine", []string{"effort=high", "retries=3", "base_url="})

	if err != nil || string(program.Config) != `{"effort":"high","retries":3}` {
		t.Fatalf("config = %s, err = %v", program.Config, err)
	}

	if _, err := configureProgram(database, store.KindProvider, "mine", []string{"novalue"}); err == nil {
		t.Fatal("a change without = was accepted")
	}

	if _, err := configureProgram(database, store.KindProvider, "missing", []string{"a=b"}); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("configuring a provider that is not there: err = %v", err)
	}

	// One the user added keeps its command when nothing is left of its
	// configuration, and goes when it is removed.
	if emptied, _ := configureProgram(database, store.KindProvider, "mine", []string{"effort=", "retries="}); emptied.Config != nil || len(emptied.Command) != 1 {
		t.Fatalf("with nothing chosen: %+v", emptied)
	}

	if removed, err := removeProgram(database, store.KindProvider, "mine"); err != nil || removed != "removed the provider mine" {
		t.Fatalf("removed = %q, err = %v", removed, err)
	}

	if _, err := removeProgram(database, store.KindProvider, "mine"); err == nil {
		t.Fatal("removing a provider twice reported nothing")
	}

	if err := registerProgram(database, store.KindProvider, "bad/name", []string{"run"}, nil); err == nil {
		t.Fatal("a name with a slash was accepted")
	}
}

func TestHostFunctionsForSettingsAndProviders(t *testing.T) {
	h := testHost(t)

	stored := call(t, h, "system-set", `{"key":"max_turns","value":"90"}`)

	if !strings.Contains(stored.Text, "system setting max_turns = 90, stored") {
		t.Fatalf("system-set = %q", stored.Text)
	}

	if shown := call(t, h, "system", ""); !strings.Contains(shown.Text, "max_turns            90  (stored)") {
		t.Fatalf("system = %q", shown.Text)
	}

	call(t, h, "system-unset", `{"key":"max_turns"}`)

	if shown := call(t, h, "system", ""); !strings.Contains(shown.Text, "max_turns            150  (default)") {
		t.Fatalf("after system-unset: %q", shown.Text)
	}

	if keys := call(t, h, "system-keys", ""); len(keys.Values) != len(settingSpecs) || keys.Values[0].Value != keyDefaultProvider {
		t.Fatalf("keys = %+v", keys.Values)
	}

	if added := call(t, h, "provider-add", `{"name":"mine","command":"{self} provide openai"}`); !strings.Contains(added.Text, "registered the provider mine, which started") {
		t.Fatalf("provider-add = %q", added.Text)
	}

	program, err := h.database.Program(store.KindProvider, "mine")

	if err != nil || len(program.Command) != 3 || program.Config != nil {
		t.Fatalf("registered = %+v, err = %v", program, err)
	}

	// Its options are fields of its configuration, like any other.
	configured := call(t, h, "provider-config", `{"provider":"mine","change":"effort=high"}`)

	if !strings.Contains(configured.Text, `configuration of mine: {"effort":"high"}`) || !strings.Contains(configured.Text, "the runs that start from now on") {
		t.Fatalf("provider-config = %q", configured.Text)
	}

	if removed := call(t, h, "provider-rm", `{"provider":"mine"}`); removed.Text != "removed the provider mine" {
		t.Fatalf("provider-rm = %q", removed.Text)
	}

	for name, params := range map[string]string{
		"provider-rm":      `{"provider":"first"}`,
		"provider-config":  `{"provider":"missing","change":"effort=high"}`,
		"provider-options": `{"provider":"second"}`,
		"system-set":       `{"key":"max_turns","value":"x"}`,
		"system-unset":     `{"key":"compact_at"}`,
	} {
		if _, err := h.invokeHost(context.Background(), name, []byte(params)); err == nil {
			t.Errorf("%s %s should fail", name, params)
		}
	}

	// A built-in provider cannot be removed, only what was chosen for it.
	if err := refused(t, h, "provider-rm", `{"provider":"chatgpt"}`); !strings.Contains(err, "is built in") {
		t.Fatalf("removing a built-in: %s", err)
	}
}

// Every function of the host says what it acts on, so a front end can list
// them apart.
func TestHostFunctionsSayWhatTheyActOn(t *testing.T) {
	for _, function := range describedFunctions() {
		switch function.Group {
		case protocol.GroupSystem, protocol.GroupSession, protocol.GroupProvider:
		default:
			t.Errorf("%s has the group %q", function.Name, function.Group)
		}
	}
}
