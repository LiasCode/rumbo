package tui

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/LiasCode/rumbo/internal/frontend"
	"github.com/LiasCode/rumbo/protocol"
)

// The functions /model is made of, which a host answers in the tests.
var modelFunctions = []protocol.Function{
	{Name: "use", Group: protocol.GroupSession, Params: []protocol.Option{{Name: "provider", Type: protocol.OptionString}, {Name: "model", Type: protocol.OptionString, Optional: true}}},
	{Name: "session-set", Group: protocol.GroupSession, Params: []protocol.Option{{Name: "change", Type: protocol.OptionString, Values: "session-values"}}},
	{Name: "session-values", Group: protocol.GroupSession, Lists: true},
	{Name: "providers", Group: protocol.GroupProvider, Lists: true},
	{Name: "provider-models", Group: protocol.GroupProvider, Lists: true, Params: []protocol.Option{{Name: "provider", Type: protocol.OptionString}}},
	{Name: "config", Group: protocol.GroupSystem, Description: "Show what is chosen"},
}

// modelHost answers the functions /model is made of, and keeps what it
// was asked.
func modelHost(asked *[]string) frontend.Handlers {
	return frontend.Handlers{
		Host: func(_ context.Context, name string, params json.RawMessage) (protocol.HostResult, error) {
			*asked = append(*asked, strings.TrimSpace(name+" "+string(params)))
			state := protocol.HostState{Dir: "/home/me/alpha", Provider: "toy", Model: "any"}

			switch name {
			case "providers":
				return protocol.HostResult{Values: []protocol.Value{{Value: "toy"}, {Value: "router"}}}, nil
			case "provider-models":
				return protocol.HostResult{Values: []protocol.Value{{Value: "big", Label: "the big one"}, {Value: "vendor/small"}}}, nil
			case "session-values":
				return protocol.HostResult{Values: []protocol.Value{{Value: "effort=low"}, {Value: "effort=high"}}}, nil
			}

			return protocol.HostResult{Text: "done: " + name, State: state}, nil
		},
		Describe: func(context.Context) (protocol.DescribeResult, error) {
			return protocol.DescribeResult{Provider: "toy", Model: "any"}, nil
		},
	}
}

// changed are the calls among asked that change something, past those that
// list what can be chosen.
func changed(asked []string) []string {
	return slices.DeleteFunc(slices.Clone(asked), func(each string) bool {
		return !strings.HasPrefix(each, "use ") && !strings.HasPrefix(each, "session-set ")
	})
}

// The menu offers the terminal UI's commands, and no function a command is
// made of; such a function typed under its name is no command.
func TestTheCommandsHideWhatTheyAreMadeOf(t *testing.T) {
	var asked []string

	s := staged(t, modelHost(&asked), protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any", Functions: modelFunctions})
	s.run(s.a.describe(s.a.host, ""))

	var names []string

	for _, each := range commandChoicesOf(s.a.info.Functions) {
		names = append(names, each.value)
	}

	if !slices.Contains(names, "/model") || !slices.Contains(names, "/settings") || slices.Contains(names, "/config") || slices.Contains(names, "/use") || slices.Contains(names, "/session-set") {
		t.Fatalf("the commands are %v", names)
	}

	s.typing("/use toy big")

	if len(asked) != 0 || !strings.Contains(s.screen(), "there is no command /use") {
		t.Fatalf("asked %v:\n%s", asked, s.screen())
	}

	s.typing("/help model")

	if screen := s.screen(); !inOrder(screen, "/model [provider/model] [option=value…]", "for example:", "/model effort=high") {
		t.Fatalf("the help of /model:\n%s", screen)
	}
}

// /model completes the providers, then the models of the one before the
// slash, and the options; it changes the model, taking what comes before
// a slash as a provider only when there is one of that name, and then the
// options, in turn.
func TestTheModelIsChangedByCommand(t *testing.T) {
	var asked []string

	s := staged(t, modelHost(&asked), protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any", Functions: modelFunctions})

	values := func() []string {
		var shown []string

		for _, each := range s.a.menu.shown {
			shown = append(shown, each.value)
		}

		return shown
	}

	s.text("/model ")

	if shown := values(); !slices.Equal(shown, []string{"toy/", "router/", "effort=low", "effort=high"}) {
		t.Fatalf("the first choices: %v", shown)
	}

	// Taking a provider goes on with its models.
	s.press(tea.KeyDown)
	s.press(tea.KeyTab)

	if s.a.input.String() != "/model router/" || !slices.Equal(values(), []string{"router/big", "router/vendor/small"}) {
		t.Fatalf("typed %q, offered %v", s.a.input.String(), values())
	}

	s.a.input.clear()
	s.a.menu = nil
	asked = nil
	s.typing("/model router/vendor/small effort=high")

	// The menu asked for the models of router once, as the word was typed.
	if models := slices.DeleteFunc(slices.Clone(asked), func(each string) bool { return !strings.HasPrefix(each, "provider-models") }); !slices.Equal(models, []string{`provider-models {"provider":"router"}`}) {
		t.Fatalf("asked for the models %q", models)
	}

	if want := []string{`use {"model":"vendor/small","provider":"router"}`, `session-set {"change":"effort=high"}`}; !slices.Equal(changed(asked), want) {
		t.Fatalf("asked %q", asked)
	}

	if screen := s.screen(); !inOrder(screen, "› /model router/vendor/small effort=high", "done: use", "done: session-set") || s.a.asking {
		t.Fatalf("the screen:\n%s", screen)
	}

	// A model whose name has a slash is the session's provider's.
	asked = nil
	s.typing("/model vendor/small")

	if want := []string{`use {"model":"vendor/small","provider":"toy"}`}; !slices.Equal(changed(asked), want) {
		t.Fatalf("asked %q", asked)
	}
}

// /sessions lists the sessions of the folder, marked, with when each was
// used; /resume goes to one by its id or the start of its title, and
// /delete deletes one so named, once the person agrees.
func TestTheSessionsAreReachedByCommand(t *testing.T) {
	s, folder := among(t, "s1", "s2", "s3")
	folder.sessions[1].Label = "write the docs"

	s.run(s.a.list())
	s.a.sessions.listed[1].used = s.a.now.Add(-3 * time.Hour)
	s.typing("/sessions")

	if screen := s.screen(); !inOrder(screen, "▌ s1", "s2  3h", "write the docs", "s3", "/resume <session> goes to one") {
		t.Fatalf("the sessions:\n%s", screen)
	}

	s.text("/resume ")

	if shown := s.a.menu.shown; len(shown) != 2 || shown[0].value != "s2" || shown[0].label != "write the docs" {
		t.Fatalf("the choices: %+v", shown)
	}

	s.a.input.clear()
	s.a.menu = nil
	s.typing("/resume Write")

	if s.a.session != "s2" || !slices.Equal(folder.started, []string{"/home/me/alpha s2"}) {
		t.Fatalf("shows %q, started %v", s.a.session, folder.started)
	}

	s.typing("/resume nothing like it")

	if !strings.Contains(s.screen(), "no session of this folder is \"nothing like it\"") || s.a.session != "s2" {
		t.Fatalf("the screen:\n%s", s.screen())
	}

	s.typing("/delete s3")

	if !strings.Contains(s.screen(), "This deletes “title of s3”") {
		t.Fatalf("the confirmation:\n%s", s.screen())
	}

	s.press(tea.KeyRight)
	s.press(tea.KeyEnter)

	if !slices.Equal(folder.removed, []string{"s3"}) {
		t.Fatalf("removed %v", folder.removed)
	}
}

// /branch completes the person's messages, and branches before the one
// numbered, with it in the composer, or from now.
func TestABranchIsMadeByCommand(t *testing.T) {
	s, folder := among(t, "s1")
	folder.branch = "b1"
	folder.sessions = append(folder.sessions, protocol.Value{Value: "b1", Label: "a branch"})

	s.text("/branch ")

	if shown := s.a.menu.shown; len(shown) != 1 || shown[0].value != "1" || shown[0].label != "“task of s1”" {
		t.Fatalf("the choices: %+v", shown)
	}

	s.a.input.clear()
	s.a.menu = nil
	s.typing("/branch 1")

	if !slices.Equal(folder.points, []string{`{"point":"1"}`}) || s.a.session != "b1" || s.a.input.String() != "task of s1" {
		t.Fatalf("branched %v, shows %q, typed %q", folder.points, s.a.session, s.a.input.String())
	}
}

// /login signs in to the provider, or to the extension named, which the
// menu offers; one without a sign-in says so.
func TestSigningInIsOneCommand(t *testing.T) {
	var called []string

	s := staged(t, frontend.Handlers{
		Describe: func(context.Context) (protocol.DescribeResult, error) {
			return protocol.DescribeResult{
				Provider:   "toy",
				Extensions: []protocol.ExtensionOffer{{Name: "drive", Functions: []protocol.Function{{Name: "login", Description: "Sign in to Drive"}}}},
			}, nil
		},
		Invoke: func(_ context.Context, params protocol.InvokeParams, _ protocol.Dialog) (json.RawMessage, error) {
			called = append(called, params.Target+" "+params.Extension+" "+params.Name)

			return json.Marshal("signed in")
		},
	}, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any"})

	s.typing("/login")

	if !strings.Contains(s.screen(), "toy has no login") {
		t.Fatalf("the screen:\n%s", s.screen())
	}

	s.text("/login ")

	if shown := s.a.menu.shown; len(shown) != 1 || shown[0].value != "drive" {
		t.Fatalf("the choices: %+v", shown)
	}

	s.press(tea.KeyEnter)
	s.press(tea.KeyEnter)

	if !slices.Equal(called, []string{"extension drive login"}) || !strings.Contains(s.screen(), "signed in") {
		t.Fatalf("called %v:\n%s", called, s.screen())
	}
}

// The functions /providers, /extensions, and /settings are made of.
var configFunctions = []protocol.Function{
	{Name: "config", Group: protocol.GroupSystem},
	{Name: "system-set", Group: protocol.GroupSystem, Params: []protocol.Option{{Name: "change", Type: protocol.OptionString}}},
	{Name: "system-values", Group: protocol.GroupSystem, Lists: true},
	{Name: "providers", Group: protocol.GroupProvider, Lists: true},
	{Name: "provider-show", Group: protocol.GroupProvider, Params: []protocol.Option{{Name: "provider", Type: protocol.OptionString, Optional: true}}},
	{Name: "provider-set", Group: protocol.GroupProvider, Params: []protocol.Option{{Name: "provider", Type: protocol.OptionString}, {Name: "change", Type: protocol.OptionString}}},
	{Name: "provider-values", Group: protocol.GroupProvider, Lists: true, Params: []protocol.Option{{Name: "provider", Type: protocol.OptionString}}},
	{Name: "provider-add", Group: protocol.GroupProvider, Params: []protocol.Option{{Name: "name", Type: protocol.OptionString}, {Name: "command", Type: protocol.OptionString}}},
	{Name: "provider-rm", Group: protocol.GroupProvider, Confirm: "This removes the provider.", Params: []protocol.Option{{Name: "provider", Type: protocol.OptionString}}},
	{Name: "extensions", Group: protocol.GroupExtension, Lists: true},
	{Name: "extension-add", Group: protocol.GroupExtension, Params: []protocol.Option{
		{Name: "name", Type: protocol.OptionString},
		{Name: "config", Type: protocol.OptionString, Optional: true},
		{Name: "replaces", Type: protocol.OptionString, Optional: true},
		{Name: "command", Type: protocol.OptionString},
	}},
}

// configHost answers the functions /providers, /extensions, and /settings
// are made of, and keeps what it was asked.
func configHost(asked *[]string) frontend.Handlers {
	return frontend.Handlers{
		Host: func(_ context.Context, name string, params json.RawMessage) (protocol.HostResult, error) {
			*asked = append(*asked, strings.TrimSpace(name+" "+string(params)))

			switch name {
			case "providers":
				return protocol.HostResult{Values: []protocol.Value{{Value: "toy", Description: "{self} provide toy"}, {Value: "local", Description: "llama-server"}}}, nil
			case "provider-values":
				return protocol.HostResult{Values: []protocol.Value{{Value: "base_url=", Description: "where it listens"}}}, nil
			case "system-values":
				return protocol.HostResult{Values: []protocol.Value{{Value: "default_provider=", Description: "the provider a new session uses"}}}, nil
			}

			return protocol.HostResult{Text: "done: " + name, State: protocol.HostState{Dir: "/home/me/alpha", Provider: "toy", Model: "any"}}, nil
		},
		Describe: func(context.Context) (protocol.DescribeResult, error) {
			return protocol.DescribeResult{Provider: "toy", Model: "any"}, nil
		},
	}
}

// /providers lists, shows, sets, adds, and removes providers, completing
// each word, and its wrapped functions are no commands of their own.
func TestProvidersAreSetUpByOneCommand(t *testing.T) {
	var asked []string

	s := staged(t, configHost(&asked), protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any", Functions: configFunctions})

	values := func() []string {
		var shown []string

		for _, each := range s.a.menu.shown {
			shown = append(shown, each.value+" "+each.label)
		}

		return shown
	}

	s.text("/providers ")

	if shown := values(); !slices.Equal(shown, []string{"toy the session's", "local ", "add ", "remove "}) {
		t.Fatalf("the first choices: %q", shown)
	}

	s.a.input.clear()
	s.a.menu = nil
	s.text("/providers local ")

	if shown := values(); !slices.Equal(shown, []string{"base_url= "}) {
		t.Fatalf("the changes of local: %q", shown)
	}

	s.a.input.clear()
	s.a.menu = nil
	asked = nil

	for _, line := range []string{"/providers", "/providers local", "/providers local base_url=http://x", "/providers add other {self} provide openai"} {
		s.typing(line)
	}

	want := []string{
		`providers`,
		`provider-show {"provider":"local"}`,
		`provider-set {"change":"base_url=http://x","provider":"local"}`,
		`provider-add {"command":"{self} provide openai","name":"other"}`,
	}

	// The menu asks for lists as the words are typed, among them.
	if !holdsInOrder(asked, want) {
		t.Fatalf("asked %q", asked)
	}

	// Removing one asks first.
	s.typing("/providers remove local")

	if !strings.Contains(s.screen(), "This removes the provider.") || slices.Contains(asked, `provider-rm {"provider":"local"}`) {
		t.Fatalf("the confirmation, asked %q:\n%s", asked, s.screen())
	}

	s.press(tea.KeyRight)
	s.press(tea.KeyEnter)

	if !slices.Contains(asked, `provider-rm {"provider":"local"}`) {
		t.Fatalf("asked %q", asked)
	}

	s.typing("/provider-set local base_url=y")

	if !strings.Contains(s.screen(), "there is no command /provider-set") {
		t.Fatalf("the screen:\n%s", s.screen())
	}
}

// /extensions add takes the configuration and what it replaces by name,
// and the rest as the command; /settings shows what applies, and sets
// rumbo's settings, completing them.
func TestExtensionsAndSettingsAreSetUpByOneCommandEach(t *testing.T) {
	var asked []string

	s := staged(t, configHost(&asked), protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any", Functions: configFunctions})

	s.typing(`/extensions add docs config={"url":"https://example.com/mcp"} {self} extend mcp`)

	if want := `extension-add {"command":"{self} extend mcp","config":"{\"url\":\"https://example.com/mcp\"}","name":"docs"}`; !slices.Contains(asked, want) {
		t.Fatalf("asked %q", asked)
	}

	s.text("/settings ")

	if shown := s.a.menu.shown; len(shown) != 1 || shown[0].value != "default_provider=" {
		t.Fatalf("the choices: %+v", shown)
	}

	s.a.input.clear()
	s.a.menu = nil
	asked = nil
	s.typing("/settings")
	s.typing("/settings default_provider=local")

	if !slices.Equal(changedSettings(asked), []string{`config`, `system-set {"change":"default_provider=local"}`}) {
		t.Fatalf("asked %q", asked)
	}
}

// changedSettings are the calls among asked that show or set the settings.
func changedSettings(asked []string) []string {
	return slices.DeleteFunc(slices.Clone(asked), func(each string) bool { return strings.HasPrefix(each, "system-values") })
}

// holdsInOrder reports whether all holds each of some, in order, among
// others.
func holdsInOrder(all, some []string) bool {
	for _, each := range all {
		if len(some) > 0 && each == some[0] {
			some = some[1:]
		}
	}

	return len(some) == 0
}

// What a command still takes shows after the cursor, dimmed: its
// arguments, what the words before ask for next, and nothing while a word
// is typed or the cursor is not at the end.
func TestTheComposerSaysWhatACommandStillTakes(t *testing.T) {
	s := staged(t, configHost(new([]string)), protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any", Functions: configFunctions})

	for line, want := range map[string]string{
		"/resume":                "<session>",
		"/resume ":               "<session>",
		"/resume s":              "",
		"/model ":                "[provider/model] [option=value…]",
		"/model x effort=high ":  "[option=value…]",
		"/providers ":            "[provider|add|remove]",
		"/providers add ":        "<name> <command…>",
		"/extensions add docs ":  "[config={…}] [replaces=tool,…] <command…>",
		"/providers remove ":     "<provider>",
		"/providers local ":      "[name=value…|function]",
		"/new":                   "",
		"a task, not a command ": "",
	} {
		s.a.input.set(line)

		if got := strings.TrimSpace(s.a.ghost()); got != want {
			t.Errorf("after %q: %q, want %q", line, got, want)
		}
	}

	s.a.menu = nil
	s.a.input.set("/resume ")
	s.a.dismissed = true

	if row := s.composerLine(); !strings.HasSuffix(row, "/resume <session>") {
		t.Fatalf("the composer shows %q", row)
	}

	s.press(tea.KeyLeft)

	if row := s.composerLine(); strings.Contains(row, "<session>") {
		t.Fatalf("with the cursor back, the composer shows %q", row)
	}
}

// composerLine is the composer's first line of text, without its border.
func (s *stage) composerLine() string {
	for _, row := range strings.Split(s.screen(), "\n") {
		if line, found := strings.CutPrefix(strings.TrimSpace(row), "│ "); found && strings.HasPrefix(line, "/") {
			return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "│"))
		}
	}

	return ""
}

// The menu of the commands shows an example of the one chosen; a command
// typed wrong is answered with the one meant; help lists the commands by
// what they are about.
func TestTheCommandsHelpWhereTheyAreTyped(t *testing.T) {
	s := staged(t, configHost(new([]string)), protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any", Functions: configFunctions})

	s.text("/mod")

	if !strings.Contains(s.screen(), "e.g. /model anthropic/claude-opus-5-5") {
		t.Fatalf("the menu:\n%s", s.screen())
	}

	s.a.input.clear()
	s.a.menu = nil

	for typed, meant := range map[string]string{"/resum": "/resume", "/sesions": "/sessions", "/prov": "/providers"} {
		s.a.menu = nil
		s.a.dismissed = true
		s.a.input.set(typed)
		s.press(tea.KeyEnter)

		if want := "did you mean " + meant + "?"; !strings.Contains(s.screen(), want) {
			t.Fatalf("after %s, no %q:\n%s", typed, want, s.screen())
		}
	}

	if help := stripped(s.a.help(nil)); !inOrder(help, "conversation and sessions", "/resume <session>", "model and account", "/model", "setting rumbo up", "/providers", "this terminal UI", "/help") {
		t.Fatalf("the help:\n%s", help)
	}
}
