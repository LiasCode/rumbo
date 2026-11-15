package tui

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

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
	u := quiet()
	u.info.Functions = []protocol.Function{
		{Name: "status", Params: []protocol.Option{{Name: "session", Optional: true}}},
		{Name: "open", Params: []protocol.Option{{Name: "session"}}},
	}

	for line, want := range map[string]bool{"/status ": false, "/open ": true, "/open 4c ": false, "/unknown ": false} {
		if got := u.needsMore(line); got != want {
			t.Errorf("needsMore(%q) = %v, want %v", line, got, want)
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

// Up and Down move the selection of an open menu, and Tab takes the one
// they moved to.
func TestTheArrowsChooseInTheMenu(t *testing.T) {
	u := quiet()

	typing(u, "/")
	shown := u.menu.shown
	u.press(key{name: keyDown})

	if chosen, _ := u.menu.current(); chosen.value != shown[1].value {
		t.Fatalf("after Down the menu is on %q, not %q", chosen.value, shown[1].value)
	}

	u.press(key{name: keyUp})
	u.press(key{name: keyUp})

	last := shown[len(shown)-1].value

	if chosen, _ := u.menu.current(); chosen.value != last {
		t.Fatalf("after Up twice the menu is on %q, not %q", chosen.value, last)
	}

	u.press(key{r: '\t'})

	if got := u.input.String(); got != last+" " {
		t.Fatalf("Tab took %q", got)
	}
}

// A menu of commands opened before the provider said what it offers has the
// provider's commands once it has.
func TestTheMenuLearnsTheProvidersCommands(t *testing.T) {
	u := quiet()
	u.described = nil

	typing(u, "/")
	u.learned(protocol.DescribeResult{Offers: protocol.Description{Functions: []protocol.Function{{Name: "greet", Description: "Say hello"}}}})

	if !slices.ContainsFunc(u.menu.shown, func(offered choice) bool { return offered.value == "/greet" }) {
		t.Fatalf("the menu holds %+v", u.menu.shown)
	}
}

// A command brought back from the history opens no menu, so Up goes on
// further back; typing opens it again.
func TestARecalledCommandLeavesTheArrowsToTheHistory(t *testing.T) {
	u := quiet()
	u.history.add("fix the parser")
	u.history.add("/help")

	u.press(key{name: keyUp})

	if u.input.String() != "/help" || u.menu != nil {
		t.Fatalf("after Up: %q, menu %v", u.input.String(), u.menu)
	}

	u.press(key{name: keyUp})

	if u.input.String() != "fix the parser" {
		t.Fatalf("after Up twice: %q", u.input.String())
	}

	u.press(key{name: keyDown})
	typing(u, " ")
	u.press(key{r: 0x7f})

	if u.menu == nil {
		t.Fatal("typing after a recalled command opens no menu")
	}
}

// Enter on a command that needs more opens the menu of what it takes next,
// which says what that is when it takes any value.
func TestEnterOnACommandThatNeedsMoreOpensItsMenu(t *testing.T) {
	u := quiet()
	u.info.Functions = []protocol.Function{
		{Name: "system-set", Params: []protocol.Option{{Name: "key"}, {Name: "value", Description: "what to store"}}},
	}

	typing(u, "/system-set")
	u.press(key{r: '\r'})
	u.wait.Wait()

	if got := u.input.String(); got != "/system-set " || u.menu == nil || u.menu.hint != "<key>" {
		t.Fatalf("after Enter: %q, menu %+v", got, u.menu)
	}

	typing(u, "max_turns")
	u.press(key{r: '\r'})
	u.wait.Wait()

	if got := u.input.String(); got != "/system-set max_turns " || u.menu == nil || u.menu.hint != "<value>  what to store" {
		t.Fatalf("after the key: %q, menu %+v", got, u.menu)
	}
}

// A function that lists values is given what was typed for the parameters
// it takes, and nothing else.
func TestValuesAreAskedWithWhatCameBefore(t *testing.T) {
	u := quiet()
	u.info.Functions = []protocol.Function{
		{Name: "provider-models", Params: []protocol.Option{{Name: "provider", Optional: true}}},
		{Name: "sessions"},
	}

	given := map[string]string{"provider": "openai", "model": "x"}

	if got := string(u.passed("provider-models", protocol.TargetHost, given)); got != `{"provider":"openai"}` {
		t.Fatalf("passed = %s", got)
	}

	if got := u.passed("sessions", protocol.TargetHost, given); got != nil {
		t.Fatalf("passed to a function that takes nothing = %s", got)
	}
}

// A name= taken from the menu waits for its value.
func TestANameIsTakenToGoOnWithItsValue(t *testing.T) {
	u := quiet()
	u.input.set("/session-options eff")
	u.menu = &menu{head: "/session-options ", choices: []choice{{value: "effort="}, {value: "effort=high"}}}
	u.menu.filter("eff")

	u.press(key{r: '\r'})

	if got := u.input.String(); got != "/session-options effort=" {
		t.Fatalf("taken: %q", got)
	}
}
