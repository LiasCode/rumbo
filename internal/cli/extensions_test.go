package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/internal/tool"
)

// extendWith is the command of the tests' extension with one tool called
// name.
func extendWith(name string) []string {
	return []string{selfPlaceholder, "extend", name}
}

// shellTool is the name of rumbo's shell tool: bash, or powershell on
// Windows.
var shellTool = tool.NewSet().Tools()[3].Def().Name

func TestAnExtensionIsRegisteredOnceItWorks(t *testing.T) {
	h := testHost(t)

	added, err := registerExtension(h.database, "finder", extendWith("find"), json.RawMessage("{}"), nil, io.Discard)

	if err != nil || added != "added the extension finder: the runs that start from now on have its tools: find" {
		t.Fatalf("added = %q, err = %v", added, err)
	}

	// One whose tool rumbo, or another extension, already has is not kept.
	for name, tool := range map[string]string{"shell": shellTool, "screen": "computer", "other": "find"} {
		_, err := registerExtension(h.database, name, extendWith(tool), nil, nil, io.Discard)

		if err == nil || !strings.Contains(err.Error(), "was not registered") || !strings.Contains(err.Error(), "already has") {
			t.Fatalf("%s: err = %v", name, err)
		}

		if _, err := h.database.Program(store.KindExtension, name); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s was kept: %v", name, err)
		}
	}

	// One registered again under its name takes its own place.
	if _, err := registerExtension(h.database, "finder", extendWith("find"), nil, nil, io.Discard); err != nil {
		t.Fatal(err)
	}

	shown, err := showExtension(h.database, "finder", io.Discard)

	if err != nil || !strings.Contains(shown, "tools, declared to the model in every request (") || !strings.Contains(shown, "find  Says where it was called.") || strings.Contains(shown, "More about it") {
		t.Fatalf("shown = %q, err = %v", shown, err)
	}
}

func TestAnExtensionsConfigurationIsCheckedByStartingIt(t *testing.T) {
	h := testHost(t)

	if _, err := registerExtension(h.database, "finder", extendWith("find"), nil, nil, io.Discard); err != nil {
		t.Fatal(err)
	}

	_, err := setProgram(h.database, store.KindExtension, "finder", []string{"broken=true"}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "finder did not start with that, so nothing was changed") || !strings.Contains(err.Error(), "broken is not something to turn on") {
		t.Fatalf("err = %v", err)
	}

	said, err := setProgram(h.database, store.KindExtension, "finder", []string{"depth=3"}, io.Discard)

	if err != nil || said[0] != "depth = 3" {
		t.Fatalf("said = %q, err = %v", said, err)
	}

	kept, err := h.database.Program(store.KindExtension, "finder")

	if err != nil || string(kept.Config) != `{"depth":3}` {
		t.Fatalf("kept = %s, err = %v", kept.Config, err)
	}
}

// A run has the tools of the extensions registered when it starts, after
// rumbo's own, and without any it has exactly rumbo's own.
func TestARunHasTheToolsOfTheExtensions(t *testing.T) {
	h := testHost(t)

	names := func() string {
		t.Helper()

		worker, err := h.agent(context.Background(), nil, h.choice, nil, nil)

		if err != nil {
			t.Fatal(err)
		}

		var names []string

		for _, each := range worker.Tools {
			names = append(names, each.Def().Name)
		}

		return strings.Join(names, " ")
	}

	if got := names(); got != "read write edit "+shellTool {
		t.Fatalf("without extensions: %q", got)
	}

	for name, tool := range map[string]string{"zeta": "alpha_tool", "alpha": "zeta_tool"} {
		if _, err := registerExtension(h.database, name, extendWith(tool), nil, nil, io.Discard); err != nil {
			t.Fatal(err)
		}
	}

	if got := names(); got != "read write edit "+shellTool+" zeta_tool alpha_tool" {
		t.Fatalf("with extensions: %q", got)
	}

	worker, _ := h.agent(context.Background(), nil, h.choice, nil, nil)
	result, err := worker.Tools[5].Run(context.Background(), "/repo/sub", json.RawMessage(`{}`))

	if err != nil || result.Text != "called in /repo/sub" {
		t.Fatalf("result = %q, err = %v", result.Text, err)
	}

	// One that stops working fails the run, saying how to take it out.
	if err := h.database.PutProgram(store.KindExtension, store.Program{Name: "zeta", Command: extendWith("alpha_tool"), Config: json.RawMessage(`{"broken":true}`)}); err != nil {
		t.Fatal(err)
	}

	_, err = h.agent(context.Background(), nil, h.choice, nil, nil)

	if err == nil || !strings.Contains(err.Error(), "extension zeta: ") || !strings.Contains(err.Error(), `"rumbo extension rm <name>" takes one out`) {
		t.Fatalf("err = %v", err)
	}

	if removed, err := removeProgram(h.database, store.KindExtension, "zeta"); err != nil || removed != "removed the extension zeta" {
		t.Fatalf("removed = %q, err = %v", removed, err)
	}

	if got := names(); got != "read write edit "+shellTool+" zeta_tool" {
		t.Fatalf("after removing one: %q", got)
	}
}

// An extension takes the place of a tool of rumbo's when the user says so,
// and only then; what it replaces is kept, shown, and given back like any
// setting.
func TestAnExtensionReplacesAToolWhenTheUserSaysSo(t *testing.T) {
	h := testHost(t)

	names := func() string {
		t.Helper()

		worker, err := h.agent(context.Background(), nil, h.choice, nil, nil)

		if err != nil {
			t.Fatal(err)
		}

		var names []string

		for _, each := range worker.Tools {
			names = append(names, each.Def().Name)
		}

		return strings.Join(names, " ")
	}

	added, err := registerExtension(h.database, "sandbox", extendWith(shellTool), nil, []string{shellTool}, io.Discard)

	if err != nil || added != "added the extension sandbox: the runs that start from now on have its tools: "+shellTool+"; its "+shellTool+" is in place of rumbo's" {
		t.Fatalf("added = %q, err = %v", added, err)
	}

	if got := names(); got != "read write edit "+shellTool {
		t.Fatalf("declared %q", got)
	}

	// The bash a run has is the extension's.
	worker, _ := h.agent(context.Background(), nil, h.choice, nil, nil)
	result, err := worker.Tools[3].Run(context.Background(), "/repo", json.RawMessage(`{}`))

	if err != nil || result.Text != "called in /repo" {
		t.Fatalf("the shell tool = %q, err = %v", result.Text, err)
	}

	kept, _ := h.database.Program(store.KindExtension, "sandbox")

	if strings.Join(kept.Replaces, " ") != shellTool {
		t.Fatalf("kept %q", kept.Replaces)
	}

	if shown, err := describeConfig(h.database, nil, ""); err != nil || !strings.Contains(shown, "replaces") {
		t.Fatalf("config = %q, err = %v", shown, err)
	}

	// Giving it back leaves a tool rumbo already has, so it is refused.
	if _, err := setProgram(h.database, store.KindExtension, "sandbox", []string{"replaces="}, io.Discard); err == nil || !strings.Contains(err.Error(), "set replaces="+shellTool+" for it") {
		t.Fatalf("err = %v", err)
	}

	if _, err := setProgram(h.database, store.KindExtension, "sandbox", []string{"replaces=" + shellTool + ",read"}, io.Discard); err == nil || !strings.Contains(err.Error(), `replaces "read", and declares no tool of that name`) {
		t.Fatalf("err = %v", err)
	}
}

// A run is told the sections of its extensions after rumbo's own.
func TestARunIsToldTheSectionsOfTheExtensions(t *testing.T) {
	h := testHost(t)

	added, err := registerExtension(h.database, "finder", extendWith("find"), json.RawMessage(`{"told":true}`), nil, io.Discard)

	if err != nil || !strings.HasSuffix(added, "have its tools: find; the model is told its sections: where") {
		t.Fatalf("added = %q, err = %v", added, err)
	}

	worker, err := h.agent(context.Background(), nil, h.choice, nil, nil)

	if err != nil || len(worker.Collectors) != 4 {
		t.Fatalf("collectors = %d, err = %v", len(worker.Collectors), err)
	}

	if last := worker.Collectors[3]; last.Name() != "where" || last.Collect(context.Background(), "/repo/sub") != "you are in /repo/sub" {
		t.Fatalf("the last section is %s", last.Name())
	}

	if shown, err := showExtension(h.database, "finder", io.Discard); err != nil || !strings.Contains(shown, "sections, told to the model when a run starts: where") {
		t.Fatalf("shown = %q, err = %v", shown, err)
	}
}

// A front end registers, shows, sets, and removes extensions through the
// host's functions, which go through what the command line does.
func TestTheHostsFunctionsManageTheExtensions(t *testing.T) {
	h := testHost(t)

	if listed := call(t, h, "extensions", ""); listed.Text != "there are no extensions yet; register one to give the model tools" || len(listed.Values) != 0 {
		t.Fatalf("extensions = %+v", listed)
	}

	added := call(t, h, "extension-add", `{"name":"finder","command":"{self} extend find","config":"{\"depth\":2}"}`)

	if added.Text != "added the extension finder: the runs that start from now on have its tools: find" {
		t.Fatalf("extension-add = %q", added.Text)
	}

	if listed := call(t, h, "extensions", ""); shownValues(listed.Values) != "finder:" || listed.Values[0].Description != "{self} extend find" {
		t.Fatalf("extensions = %+v", listed.Values)
	}

	if shown := call(t, h, "extension-show", `{"extension":"finder"}`); !strings.Contains(shown.Text, `configuration: {"depth":2}`) || !strings.Contains(shown.Text, "find  Says where it was called.") {
		t.Fatalf("extension-show = %q", shown.Text)
	}

	// A change it does not start with is refused, and nothing is kept.
	if err := refused(t, h, "extension-set", `{"extension":"finder","change":"broken=true"}`); !strings.Contains(err, "nothing was changed") {
		t.Fatalf("extension-set broken: %s", err)
	}

	if set := call(t, h, "extension-set", `{"extension":"finder","change":"depth=3"}`); set.Text != "depth = 3\nkept in config.yaml; it applies to the runs that start from now on" {
		t.Fatalf("extension-set = %q", set.Text)
	}

	values := call(t, h, "extension-values", `{"extension":"finder"}`)

	if shownValues(values.Values) != "depth=: | replaces=:" || !strings.HasPrefix(values.Values[0].Description, "in force: 3") || !strings.Contains(values.Values[1].Description, shellTool) {
		t.Fatalf("extension-values = %+v", values.Values)
	}

	if err := refused(t, h, "extension-values", `{"extension":"nowhere"}`); !strings.Contains(err, "nowhere") {
		t.Fatalf("extension-values nowhere: %s", err)
	}

	if removed := call(t, h, "extension-rm", `{"extension":"finder"}`); removed.Text != "removed the extension finder" {
		t.Fatalf("extension-rm = %q", removed.Text)
	}

	if _, err := h.database.Program(store.KindExtension, "finder"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("finder was kept: %v", err)
	}
}
