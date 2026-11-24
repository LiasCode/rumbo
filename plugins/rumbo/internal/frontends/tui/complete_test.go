package tui

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/LiasCode/rumbo/internal/frontend"
	"github.com/LiasCode/rumbo/protocol"
)

func TestOnlyASlashAndAWordIsACommand(t *testing.T) {
	for line, want := range map[string]bool{
		"/help":                  true,
		"/open 4cf0261c":         true,
		"/etc/hosts explain it":  false,
		"/":                      false,
		"explain /help":          false,
		"/Users/me/file.txt fix": false,
	} {
		if got := isCommand(line); got != want {
			t.Errorf("isCommand(%q) = %v, want %v", line, got, want)
		}
	}

	// The menu opens as soon as a command starts, and not for a path or a
	// text of several lines.
	for line, want := range map[string]bool{"/": true, "/op": true, "/open 4c": true, "/etc/hosts x": false, "/help\nmore": false, "fix": false} {
		if got := isCommandStart(line); got != want {
			t.Errorf("isCommandStart(%q) = %v, want %v", line, got, want)
		}
	}

	if head, token := splitLast("/open 4c"); head != "/open " || token != "4c" {
		t.Fatalf("splitLast = %q, %q", head, token)
	}
}

// The menu keeps the choices whose value starts with what is typed, or
// whose label holds it, and its selection goes round at either end.
func TestTheMenuNarrowsAndMoves(t *testing.T) {
	offered := &menu{choices: []choice{
		{value: "4cf0261c", label: "fix the parser"},
		{value: "57ffcc97", label: "write the docs"},
		{value: "4d1e0a2b", label: "Parse the flags"},
	}}

	offered.filter("4")

	if len(offered.shown) != 2 || offered.shown[1].value != "4d1e0a2b" {
		t.Fatalf("by value: %+v", offered.shown)
	}

	offered.filter("pars")

	if len(offered.shown) != 2 || offered.shown[0].value != "4cf0261c" {
		t.Fatalf("by label: %+v", offered.shown)
	}

	offered.move(-1)

	if chosen, _ := offered.current(); chosen.value != "4d1e0a2b" {
		t.Fatalf("up from the first: %+v", chosen)
	}

	offered.filter("")
	rows := offered.rows(30)

	if len(rows) != 3 || !strings.Contains(rows[0], "▸") || visibleWidth(rows[1]) > 30 {
		t.Fatalf("rows = %q", rows)
	}

	if (&menu{loading: true}).rows(30)[0] == "" || (&menu{hint: "no provider"}).rows(30) == nil {
		t.Fatal("a menu waiting for its values, or with none, says nothing")
	}
}

// A command taken from the menu is sent at once when its function needs
// nothing more.
func TestACommandNeedsWhatItsFunctionRequires(t *testing.T) {
	functions := []protocol.Function{
		{Name: "status", Params: []protocol.Option{{Name: "session", Optional: true}}},
		{Name: "open", Params: []protocol.Option{{Name: "session"}}},
	}

	for line, want := range map[string]bool{"/status ": false, "/open ": true, "/open 4c ": false, "/unknown ": false} {
		if got := lacksParams(functions, nil, line); got != want {
			t.Errorf("lacksParams(%q) = %v, want %v", line, got, want)
		}
	}
}

// Arguments typed after a command become the function's parameters, in its
// order, in the types it declares; the last one takes the rest of the line.
func TestArgumentsFollowTheFunctionsDescription(t *testing.T) {
	function := protocol.Function{Name: "search", Params: []protocol.Option{
		{Name: "limit", Type: protocol.OptionNumber},
		{Name: "exact", Type: protocol.OptionBoolean},
		{Name: "query", Type: protocol.OptionString},
	}}

	raw, err := arguments(function, []string{"5", "true", "two", "words"})

	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	json.Unmarshal(raw, &got)

	if want := map[string]any{"limit": 5.0, "exact": true, "query": "two words"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %v, want %v", got, want)
	}

	if _, err := arguments(function, []string{"5"}); err == nil {
		t.Fatal("missing arguments were accepted")
	}

	if _, err := arguments(function, []string{"many", "true", "x"}); err == nil {
		t.Fatal("a word was accepted as a number")
	}
}

// A parameter that can be left out and comes before one that cannot is
// typed by name, so the words of the needed ones stay theirs, and the last
// still takes the rest of the line. Any that can be left out can be typed by
// name.
func TestArgumentsTypedByName(t *testing.T) {
	add := protocol.Function{Name: "extension-add", Params: []protocol.Option{
		{Name: "name", Type: protocol.OptionString},
		{Name: "config", Type: protocol.OptionString, Optional: true},
		{Name: "replaces", Type: protocol.OptionString, Optional: true},
		{Name: "command", Type: protocol.OptionString},
	}}

	prune := protocol.Function{Name: "prune", Params: []protocol.Option{
		{Name: "age", Type: protocol.OptionString},
		{Name: "confirm", Type: protocol.OptionString, Optional: true},
	}}

	for _, each := range []struct {
		function protocol.Function
		line     string
		want     map[string]any
	}{
		{add, "files {self} extend mcp npx -y server", map[string]any{"name": "files", "command": "{self} extend mcp npx -y server"}},
		{add, `docs config={"url":"https://example.com/mcp"} {self} extend mcp`, map[string]any{"name": "docs", "config": `{"url":"https://example.com/mcp"}`, "command": "{self} extend mcp"}},
		{add, "shell replaces=bash sandbox run config=x", map[string]any{"name": "shell", "replaces": "bash", "command": "sandbox run config=x"}},
		{prune, "30d yes", map[string]any{"age": "30d", "confirm": "yes"}},
		{prune, "30d confirm=yes", map[string]any{"age": "30d", "confirm": "yes"}},
		{prune, "confirm=yes 30d", map[string]any{"age": "30d", "confirm": "yes"}},
	} {
		raw, err := arguments(each.function, strings.Fields(each.line))

		if err != nil {
			t.Fatalf("%s: %v", each.line, err)
		}

		var got map[string]any
		json.Unmarshal(raw, &got)

		if !reflect.DeepEqual(got, each.want) {
			t.Errorf("%s: arguments = %v, want %v", each.line, got, each.want)
		}
	}

	_, err := arguments(add, []string{"docs", `config={}`})

	if err == nil || !strings.Contains(err.Error(), "/extension-add <name> [config=…] [replaces=…] <command>") {
		t.Fatalf("err = %v", err)
	}
}

// Up and Down move the selection of an open menu, and Tab takes the one
// they moved to.
func TestTheArrowsChooseInTheMenu(t *testing.T) {
	s := composing(t)

	s.text("/")
	shown := s.a.menu.shown
	s.press(tea.KeyDown)

	if chosen, _ := s.a.menu.current(); chosen.value != shown[1].value {
		t.Fatalf("after Down the menu is on %q, not %q", chosen.value, shown[1].value)
	}

	s.press(tea.KeyUp)
	s.press(tea.KeyUp)

	last := shown[len(shown)-1].value

	if chosen, _ := s.a.menu.current(); chosen.value != last {
		t.Fatalf("after Up twice the menu is on %q, not %q", chosen.value, last)
	}

	s.press(tea.KeyTab)

	if got := s.a.input.String(); got != last+" " {
		t.Fatalf("Tab took %q", got)
	}
}

// A menu of commands opened before the provider said what it offers has the
// provider's commands once it has.
func TestTheMenuLearnsTheProvidersCommands(t *testing.T) {
	s := composing(t)

	s.text("/")
	s.apply(describedMsg{host: s.a.host, described: protocol.DescribeResult{Offers: protocol.Description{Functions: []protocol.Function{{Name: "greet", Description: "Say hello"}}}}})

	if !slices.ContainsFunc(s.a.menu.shown, func(offered choice) bool { return offered.value == "/greet" }) {
		t.Fatalf("the menu holds %+v", s.a.menu.shown)
	}
}

// A function there to list the values a parameter takes is no command: the
// menu offers its values instead, and help leaves it out.
func TestAFunctionThatListsValuesIsNoCommand(t *testing.T) {
	functions := []protocol.Function{
		{Name: "session-set", Description: "Choose a value", Params: []protocol.Option{{Name: "change", Values: "session-values"}}},
		{Name: "session-values", Description: "List the changes session-set takes", Lists: true},
	}

	s := staged(t, frontend.Handlers{}, protocol.FrontendInitializeParams{Provider: "toy", Functions: functions})
	s.text("/session-")

	if shown := s.a.menu.shown; len(shown) != 1 || shown[0].value != "/session-set" {
		t.Fatalf("the menu holds %+v", shown)
	}

	if listed := commands(functions); len(listed) != 1 || listed[0].Name != "session-set" {
		t.Fatalf("the commands are %+v", listed)
	}
}

// A command brought back from the history opens no menu, so Up goes on
// further back; typing opens it again.
func TestARecalledCommandLeavesTheArrowsToTheHistory(t *testing.T) {
	s := composing(t)
	s.a.history.add("fix the parser")
	s.a.history.add("/help")

	s.press(tea.KeyUp)

	if s.a.input.String() != "/help" || s.a.menu != nil {
		t.Fatalf("after Up: %q, menu %v", s.a.input.String(), s.a.menu)
	}

	s.press(tea.KeyUp)

	if s.a.input.String() != "fix the parser" {
		t.Fatalf("after Up twice: %q", s.a.input.String())
	}

	s.press(tea.KeyDown)
	s.text(" ")
	s.press(tea.KeyBackspace)

	if s.a.menu == nil {
		t.Fatal("typing after a recalled command opens no menu")
	}
}

// Enter on a command that needs more opens the menu of what it takes next,
// which says what that is when it takes any value.
func TestEnterOnACommandThatNeedsMoreOpensItsMenu(t *testing.T) {
	functions := []protocol.Function{
		{Name: "system-set", Params: []protocol.Option{{Name: "key"}, {Name: "value", Description: "what to store"}}},
	}

	s := staged(t, frontend.Handlers{}, protocol.FrontendInitializeParams{Provider: "toy", Functions: functions})
	s.typing("/system-set")

	if got := s.a.input.String(); got != "/system-set " || s.a.menu == nil || s.a.menu.hint != "<key>" {
		t.Fatalf("after Enter: %q, menu %+v", got, s.a.menu)
	}

	s.typing("max_turns")

	if got := s.a.input.String(); got != "/system-set max_turns " || s.a.menu == nil || s.a.menu.hint != "<value>  what to store" {
		t.Fatalf("after the key: %q, menu %+v", got, s.a.menu)
	}
}

// A function that lists values is given what was typed for the parameters
// it takes, and nothing else.
func TestValuesAreAskedWithWhatCameBefore(t *testing.T) {
	functions := []protocol.Function{
		{Name: "provider-models", Params: []protocol.Option{{Name: "provider", Optional: true}}},
		{Name: "sessions"},
	}

	given := map[string]string{"provider": "openai", "model": "x"}

	if got := string(passedTo(functions, "provider-models", given)); got != `{"provider":"openai"}` {
		t.Fatalf("passed = %s", got)
	}

	if got := passedTo(functions, "sessions", given); got != nil {
		t.Fatalf("passed to a function that takes nothing = %s", got)
	}
}

// A name= taken from the menu waits for its value.
func TestANameIsTakenToGoOnWithItsValue(t *testing.T) {
	s := composing(t)
	s.a.input.set("/session-set eff")
	s.a.menu = &menu{head: "/session-set ", choices: []choice{{value: "effort="}, {value: "effort=high"}}}
	s.a.menu.filter("eff")

	s.press(tea.KeyEnter)

	if got := s.a.input.String(); got != "/session-set effort=" {
		t.Fatalf("taken: %q", got)
	}
}
