package cli

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LiasCode/rumbo/internal/agent"
	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/protocol"
)

func testStore(t *testing.T) store.Store {
	t.Helper()

	database, err := store.Open(filepath.Join(t.TempDir(), "test.db"))

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { database.Close() })

	if err := seedBuiltins(database); err != nil {
		t.Fatal(err)
	}

	return database
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

	// The settings that are no longer say what took their place.
	for key, hint := range map[string]string{
		"model":             "default_provider and default_model",
		"options.anthropic": "rumbo provider options anthropic",
	} {
		for _, refused := range []error{setSetting(database, key, "x"), unsetSetting(database, key)} {
			if refused == nil || !strings.Contains(refused.Error(), hint) {
				t.Fatalf("%s: err = %v", key, refused)
			}
		}
	}

	setSetting(database, "default_provider", "anthropic")
	setSetting(database, "default_model", "org/big-model")

	// One stored for a project by an earlier version is no longer read, and
	// is listed as such.
	database.SetSetting("/repo", "max_turns", "40")

	if turns, _ := effectiveSetting(database, "max_turns"); turns.Value != "150" || turns.Source != sourceDefault {
		t.Fatalf("max_turns = %+v", turns)
	}

	listed, _ := effectiveSettings(database)
	ignored, _ := ignoredSettings(database)
	text := describeSettings(listed, ignored)

	for _, line := range []string{
		"default_provider     anthropic  (stored)",
		"default_model        org/big-model  (stored)",
		"frontend             (not set)",
		"max_turns            150  (default)",
		"compact_at           (not set)",
		"no longer read",
		"max_turns = 40  (/repo)",
	} {
		if !strings.Contains(text, line) {
			t.Errorf("missing %q in:\n%s", line, text)
		}
	}

	if strings.Contains(text, keySeeded) {
		t.Fatalf("an internal key is listed as a setting:\n%s", text)
	}

	if _, err := storedSetting(database, keySeeded); err == nil {
		t.Fatal("an internal key was read as a system setting")
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

	if err != nil || limits.MaxTurns != 150 || limits.Compaction != (agent.Compaction{}) || limits.MaxTokens != 0 {
		t.Fatalf("limits = %+v, err = %v", limits, err)
	}

	// One that is malformed in the store stops rumbo before it starts.
	database.SetSetting(store.GlobalScope, "compact_at", "90000")

	if _, err := loadConfig(database, "plain"); err == nil || !strings.Contains(err.Error(), "compact_at") {
		t.Fatalf("a malformed compact_at: err = %v", err)
	}
}

// The model an earlier version stored, with its provider, becomes the
// default provider and model, once; what was stored stays.
func TestTheOldModelSettingIsCarriedOverOnce(t *testing.T) {
	database := testStore(t)
	database.SetSetting(store.GlobalScope, legacyModel, "openai/org/model-x")

	if err := upgradeSettings(database); err != nil {
		t.Fatal(err)
	}

	loaded, _ := loadConfig(database, "plain")

	if loaded.DefaultProvider != "openai" || loaded.DefaultModel != "org/model-x" {
		t.Fatalf("carried over as %+v", loaded)
	}

	if old, found, _ := database.Setting(store.GlobalScope, legacyModel); !found || old != "openai/org/model-x" {
		t.Fatalf("the old value was not left as it was: %q", old)
	}

	// Once is enough: a default removed afterwards stays removed.
	unsetSetting(database, keyDefaultProvider)
	upgradeSettings(database)

	if _, found, _ := database.Setting(store.GlobalScope, keyDefaultProvider); found {
		t.Fatal("the old model was carried over again")
	}
}

// A provider's options are kept with its registration, and changed one by
// one.
func TestProviderOptionsAreChangedOneByOne(t *testing.T) {
	database := testStore(t)
	database.PutProgram(store.KindProvider, store.Program{Name: "anthropic", Command: []string{"run"}, Options: json.RawMessage(`{"effort":"low","beta":true}`)})

	if err := changeOptions(database, "anthropic", []string{"beta=", "depth=3"}); err != nil {
		t.Fatal(err)
	}

	if program, _ := database.Program(store.KindProvider, "anthropic"); string(program.Options) != `{"depth":3,"effort":"low"}` {
		t.Fatalf("stored options = %s", program.Options)
	}

	// The session's are over the provider's.
	inForce, err := optionsInForce(database, "anthropic", []byte(`{"depth":5,"web":true}`))

	if err != nil || string(inForce) != `{"depth":5,"effort":"low","web":true}` {
		t.Fatalf("options in force = %s, err = %v", inForce, err)
	}

	if described, _ := describeOptions(database, "anthropic"); described != `options of anthropic: {"depth":3,"effort":"low"}` {
		t.Fatalf("described = %q", described)
	}

	for name, changes := range map[string][]string{"missing": {"effort=high"}, "anthropic": {"novalue"}} {
		if err := changeOptions(database, name, changes); err == nil {
			t.Errorf("%s %v was accepted", name, changes)
		}
	}
}

// What a program says its defaults are fills what its registration does not
// hold, and changes nothing that it does.
func TestDefaultsAreKeptForWhatIsNotStored(t *testing.T) {
	database := testStore(t)
	database.PutProgram(store.KindProvider, store.Program{
		Name: "mine", Command: []string{"run"},
		Config:  json.RawMessage(`{"base_url":"http://mine"}`),
		Options: json.RawMessage(`{"effort":"high"}`),
	})

	described := protocol.Description{Options: []protocol.Option{
		{Name: "effort", Default: json.RawMessage(`""`)},
		{Name: "web", Default: json.RawMessage(`false`)},
		{Name: "style"},
	}}

	undecided, err := adopt(database, store.KindProvider, "mine", json.RawMessage(`{"base_url":"https://default","api":"chat"}`), described)

	if err != nil || len(undecided) != 1 || undecided[0] != "style" {
		t.Fatalf("undecided = %v, err = %v", undecided, err)
	}

	program, _ := database.Program(store.KindProvider, "mine")

	if string(program.Config) != `{"api":"chat","base_url":"http://mine"}` || string(program.Options) != `{"effort":"high","web":false}` {
		t.Fatalf("registered with %s and %s", program.Config, program.Options)
	}

	// A front end has a configuration and no options.
	database.PutProgram(store.KindFrontend, store.Program{Name: "editor", Command: []string{"run"}})
	adopt(database, store.KindFrontend, "editor", json.RawMessage(`{"theme":"dark"}`), protocol.Description{})

	if editor, _ := database.Program(store.KindFrontend, "editor"); string(editor.Config) != `{"theme":"dark"}` || editor.Options != nil {
		t.Fatalf("front end registered with %s and %s", editor.Config, editor.Options)
	}
}

// A provider is started when it is added, which is the test that it works,
// and stored with the defaults it says.
func TestAProviderIsStartedWhenAdded(t *testing.T) {
	database := testStore(t)

	undecided, err := registerProvider(database, "local", []string{selfPlaceholder, "provide", "openai"}, json.RawMessage(`{"base_url":"http://localhost:11434/v1"}`), io.Discard)

	if err != nil || len(undecided) != 0 {
		t.Fatalf("undecided = %v, err = %v", undecided, err)
	}

	program, _ := database.Program(store.KindProvider, "local")

	if string(program.Config) != `{"api":"chat","api_key_env":"OPENAI_API_KEY","base_url":"http://localhost:11434/v1","context_window":0,"effort":""}` || string(program.Options) != `{"effort":""}` {
		t.Fatalf("registered with %s and %s", program.Config, program.Options)
	}

	// One that does not start is not registered.
	if _, err := registerProvider(database, "broken", []string{"/no/such/program"}, nil, io.Discard); err == nil || !strings.Contains(err.Error(), "was not registered") {
		t.Fatalf("a provider that does not start: err = %v", err)
	}

	if _, err := database.Program(store.KindProvider, "broken"); err == nil {
		t.Fatal("a provider that does not start was registered")
	}
}

// The options an earlier version stored as a setting of their own are kept
// with the provider's registration, once.
func TestOldProviderOptionsAreCarriedOver(t *testing.T) {
	database := testStore(t)
	database.SetSetting(store.GlobalScope, keyUpgraded, "1")
	database.SetSetting(store.GlobalScope, "options.anthropic", `{"effort":"max"}`)

	if err := upgradeSettings(database); err != nil {
		t.Fatal(err)
	}

	if program, _ := database.Program(store.KindProvider, "anthropic"); string(program.Options) != `{"effort":"max"}` {
		t.Fatalf("carried over as %s", program.Options)
	}

	if done, _, _ := database.Setting(store.GlobalScope, keyUpgraded); done != "2" {
		t.Fatalf("the upgrades taken are recorded as %q", done)
	}
}

// A built-in program the user removed is not put back, and one added by a
// later version of rumbo still arrives.
func TestBuiltinsAreRegisteredOnce(t *testing.T) {
	database := testStore(t)

	// The openai one speaks the Responses API, which OpenAI's own models
	// need; the others need nothing.
	for name, want := range map[string]string{"openai": `{"api":"responses"}`, "anthropic": "{}"} {
		if program, err := database.Program(store.KindProvider, name); err != nil || string(program.Config) != want {
			t.Fatalf("%s registered with %q, err %v; want %q", name, program.Config, err, want)
		}
	}

	if err := database.DeleteProgram(store.KindProvider, "openai"); err != nil {
		t.Fatal(err)
	}

	if err := seedBuiltins(database); err != nil {
		t.Fatal(err)
	}

	if _, err := database.Program(store.KindProvider, "openai"); err == nil {
		t.Fatal("a removed built-in came back")
	}

	builtins[0].names = append(builtins[0].names, "newcomer")
	defer func() { builtins[0].names = builtins[0].names[:len(builtins[0].names)-1] }()

	seedBuiltins(database)

	if _, err := database.Program(store.KindProvider, "newcomer"); err != nil {
		t.Fatal("a built-in added later was not registered")
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

	if err := registerProgram(database, store.KindProvider, "bad/name", []string{"run"}, nil); err == nil {
		t.Fatal("a name with a slash was accepted")
	}
}

func TestHostFunctionsForSettingsAndProviders(t *testing.T) {
	h := testHost(t)
	seedBuiltins(h.database)

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

	if added := call(t, h, "provider-add", `{"name":"mine","command":"{self} provide openai"}`); !strings.Contains(added.Text, "which started, with its defaults") {
		t.Fatalf("provider-add = %q", added.Text)
	}

	program, err := h.database.Program(store.KindProvider, "mine")

	if err != nil || len(program.Command) != 3 || !strings.Contains(string(program.Config), `"api_key_env":"OPENAI_API_KEY"`) {
		t.Fatalf("registered = %+v, err = %v", program, err)
	}

	if configured := call(t, h, "provider-config", `{"provider":"mine","change":"effort=high"}`); !strings.Contains(configured.Text, `"effort":"high"`) {
		t.Fatalf("provider-config = %q", configured.Text)
	}

	// Its options are the provider's own, apart from its configuration.
	if shown := call(t, h, "provider-options", `{"provider":"second"}`); shown.Text != `options of second: {"depth":2}` {
		t.Fatalf("provider-options = %q", shown.Text)
	}

	call(t, h, "provider-options", `{"provider":"mine","change":"effort=low"}`)

	if mine, _ := h.database.Program(store.KindProvider, "mine"); string(mine.Options) != `{"effort":"low"}` {
		t.Fatalf("options stored with the provider = %s", mine.Options)
	}

	call(t, h, "provider-rm", `{"provider":"mine"}`)

	for name, params := range map[string]string{
		"provider-rm":      `{"provider":"first"}`,
		"provider-options": `{"provider":"missing"}`,
		"system-set":       `{"key":"max_turns","value":"x"}`,
		"system-unset":     `{"key":"compact_at"}`,
	} {
		if _, err := h.invokeHost(context.Background(), name, []byte(params)); err == nil {
			t.Errorf("%s %s should fail", name, params)
		}
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
