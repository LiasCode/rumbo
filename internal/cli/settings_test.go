package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LiasCode/rumbo/internal/agent"
	"github.com/LiasCode/rumbo/internal/store"
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

	setSetting(database, store.GlobalScope, "model", "anthropic/big")
	setSetting(database, "/repo", "model", "openai/small")
	setSetting(database, store.GlobalScope, "options.anthropic", `{"effort":"low"}`)

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
		`options.anthropic    {"effort":"low"}  (stored)`,
	} {
		if !strings.Contains(text, line) {
			t.Errorf("missing %q in:\n%s", line, text)
		}
	}

	if strings.Contains(text, keySeeded) {
		t.Fatal("an internal key is listed as a setting")
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

	if err != nil || loaded.Provider != "anthropic" || loaded.MaxTurns != 150 || loaded.Compaction != (agent.Compaction{}) || loaded.MaxTokens != 0 {
		t.Fatalf("loaded = %+v, err = %v", loaded, err)
	}
}

// A built-in program the user removed is not put back, and one added by a
// later version of rumbo still arrives.
func TestBuiltinsAreRegisteredOnce(t *testing.T) {
	database := testStore(t)

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

	call(t, h, "setting", `{"key":"max_turns","value":"40"}`)
	call(t, h, "setting", `{"key":"max_turns","value":"90","scope":"global"}`)

	if shown := call(t, h, "settings", ""); !strings.Contains(shown.Text, "max_turns            40  (project)") {
		t.Fatalf("settings = %q", shown.Text)
	}

	call(t, h, "unset", `{"key":"max_turns"}`)

	if shown := call(t, h, "settings", ""); !strings.Contains(shown.Text, "max_turns            90  (global)") {
		t.Fatalf("after unset: %q", shown.Text)
	}

	if keys := call(t, h, "setting-keys", ""); keys.Values[0].Value != "model" || keys.Values[len(keys.Values)-1].Value != "options.first" {
		t.Fatalf("keys = %+v", keys.Values)
	}

	call(t, h, "register", `{"name":"mine","command":"python3 mine.py --fast"}`)

	program, err := h.database.Program(store.KindProvider, "mine")

	if err != nil || len(program.Command) != 3 {
		t.Fatalf("registered = %+v, err = %v", program, err)
	}

	if configured := call(t, h, "configure", `{"provider":"mine","change":"effort=high"}`); !strings.Contains(configured.Text, `{"effort":"high"}`) {
		t.Fatalf("configure = %q", configured.Text)
	}

	call(t, h, "unregister", `{"provider":"mine"}`)

	for name, params := range map[string]string{
		"unregister": `{"provider":"first"}`,
		"setting":    `{"key":"max_turns","value":"x"}`,
		"unset":      `{"key":"max_turns","scope":"everywhere"}`,
	} {
		if _, err := h.invokeHost(context.Background(), name, []byte(params)); err == nil {
			t.Errorf("%s %s should fail", name, params)
		}
	}
}
