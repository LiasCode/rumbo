package cli

import (
	"context"
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
		"":      {},
		"0":     {Never: true},
		"90000": {Tokens: 90000},
		"70%":   {Percent: 70},
	} {
		if got, err := parseCompactAt(value); err != nil || got != want {
			t.Errorf("compact_at %q = %+v, %v; want %+v", value, got, err, want)
		}
	}

	for key, value := range map[string]string{
		"compact_at":      "150%",
		"model":           "nope/x",
		"max_turns":       "many",
		"frontend":        "missing",
		"options.x":       `"high"`,
		"no_such_setting": "1",
	} {
		if err := setSetting(database, store.GlobalScope, key, value); err == nil {
			t.Errorf("%s = %s was accepted", key, value)
		}
	}

	if err := setSetting(database, store.GlobalScope, "model", "missing-slash"); err == nil || !strings.Contains(err.Error(), "provider/model") {
		t.Fatalf("a model without a provider: err = %v", err)
	}

	// A provider's options are its own, not a system setting.
	for _, refused := range []error{
		setSetting(database, store.GlobalScope, "options.anthropic", `{"effort":"low"}`),
		unsetSetting(database, store.GlobalScope, "options.anthropic"),
	} {
		if refused == nil || !strings.Contains(refused.Error(), "rumbo provider options anthropic") {
			t.Fatalf("options as a system setting: err = %v", refused)
		}
	}

	setSetting(database, store.GlobalScope, "model", "anthropic/big")
	setSetting(database, "/repo", "model", "openai/small")

	// The project's value wins inside the project, and only there.
	if inside, _ := effectiveSetting(database, "/repo", "model"); inside.Value != "openai/small" || inside.Source != sourceProject {
		t.Fatalf("inside the project: %+v", inside)
	}

	if outside, _ := effectiveSetting(database, "/other", "model"); outside.Value != "anthropic/big" || outside.Source != sourceGlobal {
		t.Fatalf("outside the project: %+v", outside)
	}

	listed, err := effectiveSettings(database, "/repo")

	if err != nil {
		t.Fatal(err)
	}

	text := describeSettings(listed)

	for _, line := range []string{
		"model                openai/small  (project)",
		"frontend             (not set)",
		"max_turns            150  (default)",
		"compact_at           (not set)",
	} {
		if !strings.Contains(text, line) {
			t.Errorf("missing %q in:\n%s", line, text)
		}
	}

	if strings.Contains(text, keySeeded) || strings.Contains(text, optionsPrefix) {
		t.Fatalf("something that is not a system setting is listed as one:\n%s", text)
	}

	if _, err := storedSetting(database, store.GlobalScope, keySeeded); err == nil {
		t.Fatal("an internal key was read as a system setting")
	}

	// Removing the project's value brings the global one back.
	if err := unsetSetting(database, "/repo", "model"); err != nil {
		t.Fatal(err)
	}

	if again, _ := effectiveSetting(database, "/repo", "model"); again.Value != "anthropic/big" {
		t.Fatalf("after unset: %+v", again)
	}

	if err := unsetSetting(database, "/repo", "model"); err == nil {
		t.Fatal("removing a value that is not stored reported nothing")
	}

	loaded, err := loadConfig(database, "/repo", "", "plain", nil)

	if err != nil || loaded.Provider != "anthropic" || loaded.Model != "big" {
		t.Fatalf("loaded = %+v, err = %v", loaded, err)
	}

	limits, err := loadLimits(database, "/repo")

	if err != nil || limits.MaxTurns != 150 || limits.Compaction != (agent.Compaction{}) || limits.MaxTokens != 0 {
		t.Fatalf("limits = %+v, err = %v", limits, err)
	}

	// One that is malformed in the store stops rumbo before it starts.
	database.SetSetting("/repo", "max_turns", "many")

	if _, err := loadConfig(database, "/repo", "", "plain", nil); err == nil || !strings.Contains(err.Error(), "max_turns") {
		t.Fatalf("a malformed max_turns: err = %v", err)
	}
}

// A provider's options are stored where they always were, so the values a
// user stored before keep reaching the provider.
func TestProviderOptionsAreChangedOneByOne(t *testing.T) {
	database := testStore(t)

	if err := database.SetSetting(store.GlobalScope, "options.anthropic", `{"effort":"low","beta":true}`); err != nil {
		t.Fatal(err)
	}

	if err := changeOptions(database, "/repo", "anthropic", []string{"effort=high"}); err != nil {
		t.Fatal(err)
	}

	if err := changeOptions(database, store.GlobalScope, "anthropic", []string{"beta=", "depth=3"}); err != nil {
		t.Fatal(err)
	}

	if global, _, _ := database.Setting(store.GlobalScope, "options.anthropic"); global != `{"depth":3,"effort":"low"}` {
		t.Fatalf("global options = %s", global)
	}

	// The session's are over the stored ones, and the command line's over
	// both.
	inForce, err := optionsInForce(database, "/repo", "anthropic", []byte(`{"depth":5,"web":true}`), []byte(`{"depth":4}`))

	if err != nil || string(inForce) != `{"depth":4,"effort":"high","web":true}` {
		t.Fatalf("options in force = %s, err = %v", inForce, err)
	}

	// Removing the project's last one leaves nothing stored there, so the
	// global value applies again.
	if err := changeOptions(database, "/repo", "anthropic", []string{"effort="}); err != nil {
		t.Fatal(err)
	}

	if _, found, _ := database.Setting("/repo", "options.anthropic"); found {
		t.Fatal("an empty object was left stored")
	}

	if described, _ := describeOptions(database, "/repo", "anthropic"); described != `options of anthropic in force here: {"depth":3,"effort":"low"}` {
		t.Fatalf("described = %q", described)
	}

	for name, changes := range map[string][]string{"missing": {"effort=high"}, "anthropic": {"novalue"}} {
		if err := changeOptions(database, store.GlobalScope, name, changes); err == nil {
			t.Errorf("%s %v was accepted", name, changes)
		}
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

	// A system setting is stored for every project unless the scope says
	// this one.
	stored := call(t, h, "system-set", `{"key":"max_turns","value":"90"}`)
	call(t, h, "system-set", `{"key":"max_turns","value":"40","scope":"project"}`)

	if !strings.Contains(stored.Text, "system setting max_turns = 90, stored for every project") {
		t.Fatalf("system-set = %q", stored.Text)
	}

	if shown := call(t, h, "system", ""); !strings.Contains(shown.Text, "max_turns            40  (project)") {
		t.Fatalf("system = %q", shown.Text)
	}

	call(t, h, "system-unset", `{"key":"max_turns","scope":"project"}`)

	if shown := call(t, h, "system", ""); !strings.Contains(shown.Text, "max_turns            90  (global)") {
		t.Fatalf("after system-unset: %q", shown.Text)
	}

	if keys := call(t, h, "system-keys", ""); len(keys.Values) != len(settingSpecs) || keys.Values[0].Value != "model" {
		t.Fatalf("keys = %+v", keys.Values)
	}

	call(t, h, "provider-add", `{"name":"mine","command":"python3 mine.py --fast"}`)

	program, err := h.database.Program(store.KindProvider, "mine")

	if err != nil || len(program.Command) != 3 {
		t.Fatalf("registered = %+v, err = %v", program, err)
	}

	if configured := call(t, h, "provider-set", `{"provider":"mine","change":"effort=high"}`); !strings.Contains(configured.Text, `{"effort":"high"}`) {
		t.Fatalf("provider-set = %q", configured.Text)
	}

	// Its options are the provider's own, apart from its configuration.
	if shown := call(t, h, "provider-options", `{"provider":"second"}`); shown.Text != `options of second in force here: {"depth":2}` {
		t.Fatalf("provider-options = %q", shown.Text)
	}

	call(t, h, "provider-options", `{"provider":"mine","change":"effort=low","scope":"project"}`)

	if value, _, _ := h.database.Setting("/repo", "options.mine"); value != `{"effort":"low"}` {
		t.Fatalf("options stored for the project = %q", value)
	}

	call(t, h, "provider-rm", `{"provider":"mine"}`)

	for name, params := range map[string]string{
		"provider-rm":      `{"provider":"first"}`,
		"provider-options": `{"provider":"missing"}`,
		"system-set":       `{"key":"max_turns","value":"x"}`,
		"system-unset":     `{"key":"max_turns","scope":"everywhere"}`,
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
