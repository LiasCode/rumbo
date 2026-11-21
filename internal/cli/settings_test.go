package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/LiasCode/rumbo/internal/agent"
	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/protocol"
)

// testStore is a store in files of the test's own, with nothing chosen.
func testStore(t *testing.T) store.Store {
	t.Helper()

	// What it shows of where things are kept is the test's own too.
	t.Setenv("RUMBO_CONFIG", filepath.Join(t.TempDir(), configFile))
	t.Setenv("RUMBO_DATA", t.TempDir())

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
	if _, err := parseCompactAt("90000"); err == nil || !strings.Contains(err.Error(), "rumbo system set compact_at=80%") {
		t.Fatalf("compact_at in tokens: err = %v", err)
	}

	for change, wrong := range map[string]string{
		"compact_at=150%":       "share of the model's context",
		"default_provider=nope": `provider "nope" is not registered`,
		"max_turns=many":        "whole number",
		"computer=yes":          "on or off",
		"frontend=missing":      `front end "missing" is not registered`,
		"no_such_setting=1":     `there is no system setting "no_such_setting"`,
		"default_model=big":     `the model is now each provider's own: "rumbo provider set <provider> model=<model>"`,
		"max_turns=":            "not set in config.yaml, so there is nothing to give back",
	} {
		if _, err := setSettings(database, []string{change}); err == nil || !strings.Contains(err.Error(), wrong) {
			t.Errorf("%s: err = %v, want %q", change, err, wrong)
		}
	}

	// Every change is checked before any is stored.
	if _, err := setSettings(database, []string{"max_turns=40", "compact_at=150%"}); err == nil {
		t.Fatal("a wrong change was taken")
	}

	if _, found, _ := database.Setting(keyMaxTurns); found {
		t.Fatal("a change was stored with a wrong one beside it")
	}

	// A built-in provider is there without being registered.
	said, err := setSettings(database, []string{"default_provider=anthropic", "max_turns=40"})

	if err != nil || strings.Join(said, "\n") != "default_provider = anthropic, from the next start of rumbo\nmax_turns = 40, to the runs that start from now on" {
		t.Fatalf("said %q, err = %v", said, err)
	}

	if back, err := setSettings(database, []string{"max_turns="}); err != nil || back[0] != "max_turns: given back to its default, 150, to the runs that start from now on" {
		t.Fatalf("given back: %q, err = %v", back, err)
	}

	if turns, _ := effectiveSetting(database, keyMaxTurns); turns.Value != "150" || turns.Source != sourceDefault {
		t.Fatalf("max_turns = %+v", turns)
	}

	loaded, err := loadConfig(database, "plain")

	if err != nil || loaded.DefaultProvider != "anthropic" || loaded.Frontend != "plain" {
		t.Fatalf("loaded = %+v, err = %v", loaded, err)
	}

	limits, err := loadLimits(database)

	if err != nil || limits.MaxTurns != 150 || limits.Compaction != (agent.Compaction{Percent: 80}) || limits.MaxTokens != 0 || limits.Computer {
		t.Fatalf("limits = %+v, err = %v", limits, err)
	}

	// The computer tool is there only when the user turned it on.
	if _, err := setSettings(database, []string{"computer=on"}); err != nil {
		t.Fatal(err)
	}

	if limits, err := loadLimits(database); err != nil || !limits.Computer {
		t.Fatalf("with computer=on, limits = %+v, err = %v", limits, err)
	}

	database.DeleteSetting(keyComputer)

	// One that is malformed in the file, as a person may write it, stops
	// rumbo before it starts.
	database.SetSetting("compact_at", "90000")

	if _, err := loadConfig(database, "plain"); err == nil || !strings.Contains(err.Error(), "compact_at") {
		t.Fatalf("a malformed compact_at: err = %v", err)
	}

	// So does a name that is no setting, which is how a misspelled one
	// shows, and one there was, with what took its place.
	database.DeleteSetting("compact_at")

	for written, said := range map[string]string{
		"max_turn":      `config.yaml, under system: there is no system setting "max_turn"`,
		"default_model": "there is no system setting default_model any more: the model is now each provider's own",
	} {
		database.SetSetting(written, "40")

		if _, err := loadConfig(database, "plain"); err == nil || !strings.Contains(err.Error(), said) {
			t.Fatalf("%s in the file: err = %v", written, err)
		}

		database.DeleteSetting(written)
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

	if _, err := optionsInForce(json.RawMessage(`"fast"`), described, nil); err == nil {
		t.Fatal("a configuration that is no object was accepted")
	}
}

// A provider is started when it is added, which is the test that it works,
// and kept with what the user gave it and nothing else: its defaults are
// its own, and show says each setting with where its value comes from.
func TestAProviderIsStartedWhenAdded(t *testing.T) {
	database := testStore(t)

	if err := registerProvider(database, "local", []string{selfPlaceholder, "provide", "openai"}, json.RawMessage(`{"base_url":"http://localhost:11434/v1"}`), io.Discard); err != nil {
		t.Fatal(err)
	}

	program, _ := database.Program(store.KindProvider, "local")

	if string(program.Config) != `{"base_url":"http://localhost:11434/v1"}` {
		t.Fatalf("registered with %s", program.Config)
	}

	setProgram(database, store.KindProvider, "local", []string{"model=qwen3-coder", "effort=high"}, io.Discard)

	shown, err := showProvider(database, "local", nil, io.Discard)

	for _, line := range []string{
		"local: {self} provide openai\n",
		"model = qwen3-coder  (config.yaml)\n    the model; the one set for local is what a new session that uses it starts with\n",
		"base_url = http://localhost:11434/v1  (config.yaml)\n    The address of the server's API",
		"api = chat  (default)\n    The API it speaks",
		"    values: chat, responses\n",
		"api_key_env = OPENAI_API_KEY  (default)\n",
		"context_window = 0  (default)\n",
		"effort = high  (config.yaml)\n    an option, which a session can choose for itself. How much a reasoning model thinks",
		"\nfunctions:\n    models: List the models the server offers\n",
	} {
		if err != nil || !strings.Contains(shown, line) {
			t.Fatalf("show has no %q, err = %v:\n%s", line, err, shown)
		}
	}

	// What the session chose comes first, when it uses the provider.
	session := choice{provider: "local", model: "tiny", options: json.RawMessage(`{"effort":"low"}`)}

	if shown, _ := showProvider(database, "local", &session, io.Discard); !strings.Contains(shown, "model = tiny  (session)") || !strings.Contains(shown, "effort = low  (session)") {
		t.Fatalf("show with a session:\n%s", shown)
	}

	// A built-in says how rumbo has it.
	if shown, _ := showProvider(database, "openai", nil, io.Discard); !strings.HasPrefix(shown, "openai: {self} provide openai, built in\n") || !strings.Contains(shown, "api = responses  (built in)") {
		t.Fatalf("show of a built-in:\n%s", shown)
	}

	// One that does not start is not registered.
	if err := registerProvider(database, "broken", []string{"/no/such/program"}, nil, io.Discard); err == nil || !strings.Contains(err.Error(), "was not registered") {
		t.Fatalf("a provider that does not start: err = %v", err)
	}

	if _, err := database.Program(store.KindProvider, "broken"); err == nil {
		t.Fatal("a provider that does not start was registered")
	}
}

// A setting of a provider is checked by the provider: a name it does not
// have, a value of the wrong type or not among those it lists, and a
// configuration it does not start with are refused, and nothing is stored.
func TestSettingAProviderIsCheckedByIt(t *testing.T) {
	database := testStore(t)
	registerProgram(database, store.KindProvider, "local", []string{selfPlaceholder, "provide", "openai"}, json.RawMessage(`{"base_url":"http://a"}`))

	said, err := setProgram(database, store.KindProvider, "local", []string{"context_window=32768", "effort=3", "model=org/big", "base_url="}, io.Discard)

	if err != nil || strings.Join(said, "|") != "context_window = 32768|effort = 3|model = org/big; a new session that uses local starts with it|base_url: given back to its default" {
		t.Fatalf("said %q, err = %v", said, err)
	}

	program, _ := database.Program(store.KindProvider, "local")

	if string(program.Config) != `{"context_window":32768,"effort":"3"}` || program.Model != "org/big" || len(program.Command) != 3 {
		t.Fatalf("local is %+v", program)
	}

	for change, wrong := range map[string]string{
		"contxt_window=1":    `local has no setting "contxt_window"; its settings are model, base_url, api, api_key_env, context_window, effort`,
		"api=bogus":          `api is one of chat, responses, not "bogus"`,
		"base_url=":          "base_url is not set for local in config.yaml, so there is nothing to give back",
		"context_window=big": `context_window is a number, not "big"`,
	} {
		if _, err := setProgram(database, store.KindProvider, "local", []string{change}, io.Discard); err == nil || !strings.Contains(err.Error(), wrong) {
			t.Errorf("%s: err = %v, want %q", change, err, wrong)
		}
	}

	if after, _ := database.Program(store.KindProvider, "local"); string(after.Config) != string(program.Config) {
		t.Fatalf("a refused change was stored: %s", after.Config)
	}

	// What the provider refuses when it starts is refused too, saying why:
	// a field it does not describe is checked by the value it has.
	registerProgram(database, store.KindProvider, "loose", []string{selfPlaceholder, "provide", "openai"}, json.RawMessage(`{"api":"bogus"}`))

	if _, err := setProgram(database, store.KindProvider, "loose", []string{"api_key_env=MY_KEY"}, io.Discard); err == nil || !strings.Contains(err.Error(), `loose did not start with that, so nothing was changed: initialize provider: invalid config: api is chat or responses, not "bogus"`) {
		t.Fatalf("a provider that does not start with its configuration: err = %v", err)
	}

	// The change that makes it start is taken.
	if _, err := setProgram(database, store.KindProvider, "loose", []string{"api=chat"}, io.Discard); err != nil {
		t.Fatalf("the change that fixes it: %v", err)
	}

	// A built-in says how rumbo has a field nothing is set for.
	if _, err := setProgram(database, store.KindProvider, "openai", []string{"api="}, io.Discard); err == nil || !strings.Contains(err.Error(), "responses is how rumbo has it built in") {
		t.Fatalf("giving back a built-in field: err = %v", err)
	}

	if _, err := setProgram(database, store.KindProvider, "missing", []string{"a=b"}, io.Discard); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("setting a provider that is not there: err = %v", err)
	}

	if _, err := setProgram(database, store.KindProvider, "local", []string{"novalue"}, io.Discard); err == nil {
		t.Fatal("a change without = was accepted")
	}

	// One the user added keeps its command when nothing is left of its
	// configuration, and goes when it is removed.
	setProgram(database, store.KindProvider, "local", []string{"context_window=", "effort=", "model="}, io.Discard)

	if emptied, _ := database.Program(store.KindProvider, "local"); emptied.Config != nil || emptied.Model != "" || len(emptied.Command) != 3 {
		t.Fatalf("with nothing chosen: %+v", emptied)
	}

	if removed, err := removeProgram(database, store.KindProvider, "local"); err != nil || removed != "removed the provider local" {
		t.Fatalf("removed = %q, err = %v", removed, err)
	}

	if err := registerProgram(database, store.KindProvider, "bad/name", []string{"run"}, nil); err == nil {
		t.Fatal("a name with a slash was accepted")
	}

	// A front end cannot be started to check what it is given: it takes it
	// as written, and checks it when it starts.
	if said, err := setProgram(database, store.KindFrontend, "tui", []string{"theme=plain", "color_code=214"}, io.Discard); err != nil || len(said) != 2 {
		t.Fatalf("front end set: %q, err = %v", said, err)
	}

	if tui, _ := database.Program(store.KindFrontend, "tui"); string(tui.Config) != `{"color_code":214,"theme":"plain"}` {
		t.Fatalf("tui = %s", tui.Config)
	}
}

// The built-in programs are there without being registered. What the user
// sets for one is all that is kept of it, over what it is built in with.
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
		t.Fatalf("a built-in nothing was set for is registered: err = %v", err)
	}

	listed, err := listPrograms(database, store.KindFrontend)

	if err != nil || len(listed) != 2 || listed[0].Name != "plain" || listed[1].Name != "tui" {
		t.Fatalf("front ends = %+v, err = %v", listed, err)
	}

	if _, err := setProgram(database, store.KindProvider, "openai", []string{"effort=high", "api=chat", "model=gpt-x"}, io.Discard); err != nil {
		t.Fatal(err)
	}

	if kept, _ := database.Program(store.KindProvider, "openai"); len(kept.Command) != 0 || kept.Model != "gpt-x" || string(kept.Config) != `{"api":"chat","effort":"high"}` {
		t.Fatalf("kept of openai: %+v", kept)
	}

	// A field given back goes to what the built-in has, and nothing is kept
	// of a built-in once nothing is set for it.
	setProgram(database, store.KindProvider, "openai", []string{"api="}, io.Discard)

	if back, _ := findProgram(database, store.KindProvider, "openai"); string(back.Config) != `{"api":"responses","effort":"high"}` || back.Model != "gpt-x" {
		t.Fatalf("after giving api back: %+v", back)
	}

	setProgram(database, store.KindProvider, "openai", []string{"effort=", "model="}, io.Discard)

	if _, err := database.Program(store.KindProvider, "openai"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a built-in with nothing set is still registered: err = %v", err)
	}

	// It cannot be removed; what was set for it can.
	if _, err := removeProgram(database, store.KindProvider, "openai"); err == nil || !strings.Contains(err.Error(), "is built in") {
		t.Fatalf("removing a built-in: err = %v", err)
	}

	setProgram(database, store.KindProvider, "openai", []string{"effort=low"}, io.Discard)

	if removed, err := removeProgram(database, store.KindProvider, "openai"); err != nil || !strings.Contains(removed, "as rumbo has it again") {
		t.Fatalf("removing what was set for a built-in: %q, err = %v", removed, err)
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

// The configuration is shown all at once, each value with where it comes
// from, without starting anything, and a session's choice first.
func TestTheConfigurationSaysWhereEachValueComesFrom(t *testing.T) {
	database := testStore(t)
	setSettings(database, []string{"default_provider=openai", "max_turns=90"})
	setProgram(database, store.KindProvider, "openai", []string{"model=gpt-x", "effort=high"}, io.Discard)
	registerProgram(database, store.KindProvider, "local", []string{"python3", "provider.py"}, json.RawMessage(`{"base_url":"http://localhost"}`))
	setProgram(database, store.KindFrontend, "tui", []string{"theme=plain"}, io.Discard)

	session := sessionLayer{title: "this session, 4cf0261c", choice: choice{provider: "openai", model: "gpt-y", options: json.RawMessage(`{"effort":"low"}`)}}
	shown, err := describeConfig(database, &session, "provider-show")

	if err != nil {
		t.Fatal(err)
	}

	rows := map[string][]string{}

	for _, line := range strings.Split(shown, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			rows[fields[0]] = append(rows[fields[0]], strings.Join(fields, " "))
		}
	}

	for name, want := range map[string][]string{
		"provider":         {"provider openai session"},
		"model":            {"model gpt-y session", "model gpt-x config.yaml"},
		"effort":           {"effort low session", "effort high config.yaml"},
		"default_provider": {"default_provider openai config.yaml"},
		"frontend":         {"frontend (not set) tui in a terminal, plain otherwise"},
		"max_turns":        {"max_turns 90 config.yaml"},
		"max_tokens":       {"max_tokens (not set) the provider's own limit"},
		"compact_at":       {"compact_at 80% default"},
		"openai":           {"openai built in"},
		"api":              {"api responses built in"},
		"local":            {"local python3 provider.py"},
		"base_url":         {"base_url http://localhost config.yaml"},
		"tui":              {"tui built in"},
		"theme":            {"theme plain config.yaml"},
	} {
		if !slices.Equal(rows[name], want) {
			t.Errorf("%s: %q, want %q", name, rows[name], want)
		}
	}

	if !strings.HasSuffix(shown, "What is not listed is the program's own default; provider-show says those.") {
		t.Fatalf("the configuration ends:\n%s", shown)
	}
}

func TestHostFunctionsForSettingsAndProviders(t *testing.T) {
	h := testHost(t)

	if stored := call(t, h, "system-set", `{"change":"max_turns=90"}`); stored.Text != "max_turns = 90, to the runs that start from now on" {
		t.Fatalf("system-set = %q", stored.Text)
	}

	if shown := call(t, h, "config", ""); !strings.Contains(shown.Text, "this session, which is new") || !strings.Contains(shown.Text, "max_turns         90") {
		t.Fatalf("config = %q", shown.Text)
	}

	call(t, h, "system-set", `{"change":"max_turns="}`)

	if values := call(t, h, "system-values", "").Values; !slices.ContainsFunc(values, func(value protocol.Value) bool { return value.Value == "default_provider=anthropic" }) {
		t.Fatalf("system-values = %+v", values)
	}

	// A switch is offered with both of its values.
	for _, offered := range []string{"computer=on", "computer=off"} {
		if values := call(t, h, "system-values", "").Values; !slices.ContainsFunc(values, func(value protocol.Value) bool { return value.Value == offered }) {
			t.Fatalf("system-values has no %s: %+v", offered, values)
		}
	}

	if added := call(t, h, "provider-add", `{"name":"mine","command":"{self} provide openai"}`); !strings.Contains(added.Text, "registered the provider mine, which started") {
		t.Fatalf("provider-add = %q", added.Text)
	}

	program, err := h.database.Program(store.KindProvider, "mine")

	if err != nil || len(program.Command) != 3 || program.Config != nil {
		t.Fatalf("registered = %+v, err = %v", program, err)
	}

	// Its options are settings of it, like any other.
	if configured := call(t, h, "provider-set", `{"provider":"mine","change":"effort=high"}`); configured.Text != "effort = high\nkept in config.yaml; it applies to the runs that start from now on" {
		t.Fatalf("provider-set = %q", configured.Text)
	}

	if shown := call(t, h, "provider-show", `{"provider":"mine"}`); !strings.Contains(shown.Text, "effort = high  (config.yaml)") {
		t.Fatalf("provider-show = %q", shown.Text)
	}

	values := call(t, h, "provider-values", `{"provider":"mine"}`).Values

	for _, want := range []string{"effort=", "api=chat", "api=responses", "context_window="} {
		if !slices.ContainsFunc(values, func(value protocol.Value) bool { return value.Value == want }) {
			t.Errorf("provider-values has no %s: %+v", want, values)
		}
	}

	if removed := call(t, h, "provider-rm", `{"provider":"mine"}`); removed.Text != "removed the provider mine" {
		t.Fatalf("provider-rm = %q", removed.Text)
	}

	for name, params := range map[string]string{
		"provider-rm":      `{"provider":"first"}`,
		"provider-set":     `{"provider":"missing","change":"effort=high"}`,
		"provider-options": `{"provider":"second"}`,
		"system-set":       `{"change":"max_turns=x"}`,
		"system-unset":     `{"key":"compact_at"}`,
	} {
		if _, err := h.invokeHost(context.Background(), name, []byte(params)); err == nil {
			t.Errorf("%s %s should fail", name, params)
		}
	}

	// A built-in provider cannot be removed, only what was set for it.
	if err := refused(t, h, "provider-rm", `{"provider":"chatgpt"}`); !strings.Contains(err, "is built in") {
		t.Fatalf("removing a built-in: %s", err)
	}
}

// Every function of the host says what it acts on, so a front end can list
// them apart, and one there to list values is named by a parameter that
// takes them.
func TestHostFunctionsSayWhatTheyActOn(t *testing.T) {
	functions := describedFunctions()

	for _, function := range functions {
		switch function.Group {
		case protocol.GroupSystem, protocol.GroupSession, protocol.GroupProvider:
		default:
			t.Errorf("%s has the group %q", function.Name, function.Group)
		}

		if !function.Lists {
			continue
		}

		named := slices.ContainsFunc(functions, func(other protocol.Function) bool {
			return slices.ContainsFunc(other.Params, func(param protocol.Option) bool { return param.Values == function.Name })
		})

		if !named {
			t.Errorf("%s lists values no parameter takes", function.Name)
		}
	}
}
