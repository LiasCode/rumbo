package tui

import (
	"encoding/json"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/LiasCode/rumbo/protocol"
)

// /providers, /extensions, and /settings set rumbo up: what it reaches the
// model with, the programs that give the model tools, and the settings of
// rumbo itself. Each is one command with a few words after it, made of the
// host's functions for a kind of program: listing them, showing one,
// setting it, adding one, and removing one.

// kind is a kind of program the host keeps, as a command sets it up: the
// word for one, and the functions of the host that act on them.
type kind struct {
	command string
	// one is what a program of the kind is called, and param the name the
	// host's functions give it.
	one   string
	param string
	list  string
	show  string
	set   string
	// values lists the changes set takes; add and remove are the functions
	// that register one and remove it.
	values string
	add    string
	remove string
}

var (
	providerKind  = kind{command: "providers", one: "provider", param: "provider", list: "providers", show: "provider-show", set: "provider-set", values: "provider-values", add: "provider-add", remove: "provider-rm"}
	extensionKind = kind{command: "extensions", one: "extension", param: "extension", list: "extensions", show: "extension-show", set: "extension-set", values: "extension-values", add: "extension-add", remove: "extension-rm"}
)

// hostCall is one call of a function of the host.
type hostCall struct {
	name   string
	params map[string]string
}

// hostCalls calls functions of the host in turn, in the background, and
// shows what they said together; it stops at the first refused.
func (a *app) hostCalls(calls []hostCall) tea.Cmd {
	for _, each := range calls {
		if !a.offers(each.name) {
			a.shown.add(&entry{kind: entryAnswer, text: "this rumbo cannot " + each.name + "; it may be older than this terminal UI", failed: true})
			return nil
		}
	}

	a.asking = true

	held, conv, label := a.host, a.shown, a.label(fromHost)
	conn := held.conn

	return func() tea.Msg {
		var said []string
		var state *protocol.HostState

		for _, each := range calls {
			var raw json.RawMessage

			if len(each.params) > 0 {
				raw, _ = json.Marshal(each.params)
			}

			result, err := conn.InvokeHost(each.name, raw)

			if err != nil {
				return answeredMsg{host: held, conv: conv, label: label, text: strings.Join(said, "\n"), err: err, state: state}
			}

			said = append(said, strings.TrimSpace(result.Text+"\n"+list(result.Values)))
			state = &result.State
		}

		return answeredMsg{host: held, conv: conv, label: label, text: strings.Join(said, "\n"), state: state}
	}
}

// hostChoices lists what a function of the host lists, as choices, away
// from the screen; nothing when the host does not offer it.
func (a *app) hostChoices(name string, params map[string]string, hint string) func() ([]choice, string) {
	conn, offered := a.conn, a.offers(name)

	return func() ([]choice, string) {
		if !offered {
			return nil, hint
		}

		var raw json.RawMessage

		if len(params) > 0 {
			raw, _ = json.Marshal(params)
		}

		result, err := conn.InvokeHost(name, raw)

		if err != nil {
			return nil, err.Error()
		}

		choices := make([]choice, len(result.Values))

		for index, value := range result.Values {
			choices[index] = choice{value: value.Value, label: value.Label, description: value.Description}
		}

		if len(choices) == 0 && hint == "" {
			hint = strings.TrimSpace(result.Text)
		}

		return choices, hint
	}
}

// programVerb is the command that sets up programs of a kind.
func programVerb(k kind, description string, examples ...string) verb {
	return verb{
		name: k.command, group: groupSetUp, args: "[" + k.one + "] [name=value…|add|remove]", description: description, examples: examples,
		hint:     func(args []string) string { return k.still(args) },
		complete: func(a *app, args []string, _ string) func() ([]choice, string) { return a.programChoices(k, args) },
		run:      func(a *app, args []string) tea.Cmd { return a.programCommand(k, args) },
	}
}

// still says what is still to be typed after the words given to a kind's
// command, which depends on the first of them.
func (k kind) still(args []string) string {
	switch {
	case len(args) == 0:
		return "[" + k.one + "|add|remove]"
	case args[0] == "remove" && len(args) == 1:
		return "<" + k.one + ">"
	case args[0] == "remove":
		return ""
	case args[0] == "add" && len(args) == 1:
		return "<name> <command…>"
	case args[0] == "add" && k == extensionKind:
		return "[config={…}] [replaces=tool,…] <command…>"
	case args[0] == "add":
		return "<command…>"
	case len(args) == 1:
		return "[name=value…|function]"
	}

	return ""
}

// programChoices offers what the next word after a kind's command can be:
// add, remove, or a program, and after a program the changes it takes and
// the functions it offers.
func (a *app) programChoices(k kind, args []string) func() ([]choice, string) {
	switch {
	case len(args) == 0:
		listed := a.hostChoices(k.list, nil, "")
		current := a.info.Provider

		return func() ([]choice, string) {
			choices, hint := listed()

			for index := range choices {
				if k == providerKind && choices[index].value == current {
					choices[index].label = "the session's"
				}
			}

			choices = append(choices,
				choice{value: "add", description: "register a " + k.one + ": a name and the command that starts it"},
				choice{value: "remove", description: "remove a " + k.one + " you registered, once you agree"})

			return choices, hint
		}

	case args[0] == "remove" && len(args) == 1:
		return a.hostChoices(k.list, nil, "")

	case args[0] == "add":
		return func() ([]choice, string) {
			if len(args) == 1 {
				return nil, "<name>  what to call the " + k.one
			}

			if k == extensionKind {
				return nil, "[config={…}] [replaces=tool,…] <command…>  the program and its arguments; {self} is this rumbo"
			}

			return nil, "<command…>  the program and its arguments; {self} is this rumbo"
		}

	case args[0] == "remove":
		return nil

	// What a function the program offers takes is its own to say.
	case len(args) > 1 && !strings.Contains(args[1], "="):
		return nil
	}

	changes := a.hostChoices(k.values, map[string]string{k.param: args[0]}, "")
	functions := a.programFunctions(k, args[0])

	return func() ([]choice, string) {
		choices, hint := changes()

		// What the program offers comes after what it is set with, when
		// only a change has been given so far.
		if len(args) == 1 {
			for _, function := range functions {
				choices = append(choices, choice{value: function.Name, description: function.Description})
			}
		}

		return choices, hint
	}
}

// programFunctions are the functions a program offers that a command can
// call: the session's provider's, or the extension's, as described.
func (a *app) programFunctions(k kind, name string) []protocol.Function {
	if a.described == nil {
		return nil
	}

	if k == providerKind {
		if name != a.info.Provider {
			return nil
		}

		return commands(a.described.Offers.Functions)
	}

	for _, extension := range a.described.Extensions {
		if extension.Name == name {
			return commands(extension.Functions)
		}
	}

	return nil
}

// programCommand does what the words after a kind's command say: list the
// programs, show one, set it, add one, remove one, or call a function one
// offers.
func (a *app) programCommand(k kind, args []string) tea.Cmd {
	fail := func(why string) tea.Cmd {
		a.shown.add(&entry{kind: entryAnswer, text: why, failed: true})
		return nil
	}

	switch {
	case len(args) == 0:
		return a.hostCalls([]hostCall{{name: k.list}})

	case args[0] == "add":
		index := slices.IndexFunc(a.info.Functions, func(function protocol.Function) bool { return function.Name == k.add })

		if index < 0 {
			return fail("this rumbo cannot add a " + k.one)
		}

		if len(args) < 3 {
			return fail("usage: /" + k.command + " add <name> <command…>; /help " + k.command + " has examples")
		}

		return a.call(fromHost, a.info.Functions[index], args[1:])

	case args[0] == "remove":
		index := slices.IndexFunc(a.info.Functions, func(function protocol.Function) bool { return function.Name == k.remove })

		if index < 0 {
			return fail("this rumbo cannot remove a " + k.one)
		}

		if len(args) != 2 {
			return fail("usage: /" + k.command + " remove <" + k.one + ">")
		}

		return a.call(fromHost, a.info.Functions[index], args[1:])

	case len(args) == 1:
		return a.hostCalls([]hostCall{{name: k.show, params: map[string]string{k.param: args[0]}}})
	}

	// What the programs offer is asked first, when it is not known yet.
	if a.described == nil && !strings.Contains(args[1], "=") {
		a.asking = true
		return a.describe(a.host, "/"+k.command+" "+strings.Join(args, " "))
	}

	// A function the program offers takes the words after it.
	if functions := a.programFunctions(k, args[0]); slices.ContainsFunc(functions, func(function protocol.Function) bool { return function.Name == args[1] }) {
		function := functions[slices.IndexFunc(functions, func(function protocol.Function) bool { return function.Name == args[1] })]
		source := fromProvider

		if k == extensionKind {
			source = fromExtension(args[0])
			// An extension's function is called by its command's name.
			function.Name = args[0] + "-" + function.Name
		}

		return a.call(source, function, args[2:])
	}

	var calls []hostCall

	for _, change := range args[1:] {
		if !strings.Contains(change, "=") {
			return fail(change + " is neither a change, as name=value, nor something " + args[0] + " offers; Tab after /" + k.command + " " + args[0] + " lists both")
		}

		calls = append(calls, hostCall{name: k.set, params: map[string]string{k.param: args[0], "change": change}})
	}

	return a.hostCalls(calls)
}

// settingsVerb is the command that shows what applies and sets rumbo's own
// settings.
func settingsVerb() verb {
	return verb{
		name: "settings", group: groupSetUp, args: "[name=value…]", description: "Show what applies here and where each value comes from, or change a setting of rumbo's",
		examples: []string{"/settings", "/settings default_provider=claude", "/settings default_provider="},
		complete: func(a *app, _ []string, _ string) func() ([]choice, string) {
			return a.hostChoices("system-values", nil, "name=value; an empty value gives it back to rumbo's default")
		},
		run: func(a *app, args []string) tea.Cmd {
			if len(args) == 0 {
				return a.hostCalls([]hostCall{{name: "config"}})
			}

			var calls []hostCall

			for _, change := range args {
				if !strings.Contains(change, "=") {
					a.shown.add(&entry{kind: entryAnswer, text: "usage: /settings [name=value…]; Tab after /settings lists them", failed: true})
					return nil
				}

				calls = append(calls, hostCall{name: "system-set", params: map[string]string{"change": change}})
			}

			return a.hostCalls(calls)
		},
	}
}
