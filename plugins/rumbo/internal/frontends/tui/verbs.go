package tui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/LiasCode/rumbo/internal/frontends/render"
	"github.com/LiasCode/rumbo/protocol"
)

// The terminal UI's commands are few, and say what a person wants in their
// words: /model, /resume, /login. Each is made of the functions the host,
// the provider, and the extensions describe, which it calls by name, so a
// person never needs to know them; a command whose functions a host does not
// offer says so when it is used. The functions it does not wrap yet are
// commands of their own, under their names.

// verb is one of the terminal UI's commands.
type verb struct {
	name string
	// args are its arguments, as its usage shows them: <one> it needs,
	// [one] it can do without.
	args        string
	description string
	examples    []string
	// group is what the command is about, which help lists it under.
	group string
	// hint says what is still to be typed after the words given, when it
	// is not what args says.
	hint func(args []string) string
	// complete returns, given the words typed after the command and the
	// part of the word being typed up to its first slash, what works out the
	// choices for that word away from the screen; nil when there are none.
	complete func(a *app, args []string, stem string) func() ([]choice, string)
	run      func(a *app, args []string) tea.Cmd
}

// needs reports whether the command takes an argument it cannot do
// without.
func (v verb) needs() bool {
	return strings.HasPrefix(v.args, "<")
}

// The groups of the commands, in the order help lists them.
const (
	groupSessions = "conversation and sessions"
	groupModel    = "model and account"
	groupSetUp    = "setting rumbo up"
	groupHere     = "this terminal UI"
)

// still says what is still to be typed after the words given: the rest of
// its arguments, or the last again when it takes more of them.
func (v verb) still(args []string) string {
	if v.hint != nil {
		return v.hint(args)
	}

	parts := strings.Fields(v.args)

	switch {
	case len(args) < len(parts):
		return strings.Join(parts[len(args):], " ")
	case len(parts) > 0 && strings.Contains(parts[len(parts)-1], "…"):
		return parts[len(parts)-1]
	}

	return ""
}

// example is the example that shows the most of the command: the first
// that gives it something.
func (v verb) example() string {
	for _, each := range v.examples {
		if strings.Contains(each, " ") {
			return each
		}
	}

	if len(v.examples) > 0 {
		return v.examples[0]
	}

	return ""
}

// verbs are the terminal UI's commands, in the order help lists them.
func verbs() []verb {
	return []verb{
		{
			name: "new", group: groupSessions, description: "Start the next task in a new session",
			run: func(a *app, _ []string) tea.Cmd { return a.newSession() },
		},
		{
			name: "sessions", group: groupSessions, description: "List the sessions of this folder: which shows, which work, and when each was last used",
			run: func(a *app, _ []string) tea.Cmd { return a.listSessionsHere() },
		},
		{
			name: "resume", group: groupSessions, args: "<session>", description: "Go to another session of this folder",
			examples: []string{"/resume 4cf0261c"},
			complete: func(a *app, args []string, _ string) func() ([]choice, string) {
				return a.sessionChoices(len(args) > 0)
			},
			run: func(a *app, args []string) tea.Cmd { return a.resume(args) },
		},
		{
			name: "rename", group: groupSessions, args: "<title>", description: "Give this session a title of your own",
			examples: []string{"/rename fix the login test"},
			complete: func(*app, []string, string) func() ([]choice, string) {
				return func() ([]choice, string) { return nil, "<title>  the words that follow are its title" }
			},
			run: func(a *app, args []string) tea.Cmd {
				return a.hostCommand("rename", map[string]string{"title": strings.Join(args, " ")})
			},
		},
		{
			name: "branch", group: groupSessions, args: "[message]", description: "Go on in a new session that starts before one of your messages, or from now",
			examples: []string{"/branch", "/branch 2"},
			complete: func(a *app, args []string, _ string) func() ([]choice, string) { return a.messageChoices(args) },
			run:      func(a *app, args []string) tea.Cmd { return a.branchCommand(args) },
		},
		{
			name: "compact", group: groupSessions, description: "Replace the conversation with a summary now, to free the model's context",
			run: func(a *app, _ []string) tea.Cmd { return a.hostCommand("compact", nil) },
		},
		{
			name: "delete", group: groupSessions, args: "[session]", description: "Delete a session and its history, for good, once you agree; without one, this one",
			complete: func(a *app, args []string, _ string) func() ([]choice, string) {
				return a.sessionChoices(len(args) > 0)
			},
			run: func(a *app, args []string) tea.Cmd { return a.deleteCommand(args) },
		},
		{
			name: "copy", group: groupSessions, args: "[code|id]", description: "Copy the model's last answer, its last code block, or this session's id",
			examples: []string{"/copy", "/copy code"},
			complete: fixed(choice{value: "code", description: "only the last code block of the last answer"}, choice{value: "id", description: "this session's id"}),
			run:      func(a *app, args []string) tea.Cmd { return a.copyReply(args) },
		},
		{
			name: "export", group: groupSessions, args: "[path]", description: "Write this session as Markdown to a file, which it does not overwrite",
			complete: func(*app, []string, string) func() ([]choice, string) {
				return func() ([]choice, string) {
					return nil, "[path]  without it, rumbo-<session>.md in the session's folder"
				}
			},
			run: func(a *app, args []string) tea.Cmd { return a.export(args) },
		},
		{
			name: "status", group: groupHere, description: "Show the session beside the conversation: its model, context, tokens, and skills",
			run: func(a *app, _ []string) tea.Cmd { return a.showPanel(panelDetails) },
		},
		{
			name: "changes", group: groupHere, description: "Show what changed in the folder, as Git sees it, beside the conversation",
			run: func(a *app, _ []string) tea.Cmd { return a.showPanel(panelChanges) },
		},
		{
			name: "model", group: groupModel, args: "[provider/model] [option=value…]", description: "Change the model and its options for this session; without them, choose in a form",
			examples: []string{"/model", "/model anthropic/claude-opus-5-5", "/model effort=high"},
			complete: func(a *app, args []string, stem string) func() ([]choice, string) { return a.modelChoices(args, stem) },
			run:      func(a *app, args []string) tea.Cmd { return a.modelCommand(args) },
		},
		{
			name: "login", group: groupModel, args: "[extension]", description: "Sign in to the provider, or to an extension that asks for it",
			complete: func(a *app, args []string, _ string) func() ([]choice, string) { return a.signChoices("login", args) },
			run:      func(a *app, args []string) tea.Cmd { return a.sign("login", args) },
		},
		{
			name: "logout", group: groupModel, args: "[extension]", description: "Sign out of the provider, or of an extension",
			complete: func(a *app, args []string, _ string) func() ([]choice, string) { return a.signChoices("logout", args) },
			run:      func(a *app, args []string) tea.Cmd { return a.sign("logout", args) },
		},
		programVerb(providerKind, "List the providers, show one, set it up, add one, or remove one; what the session's offers is called after its name",
			"/providers", "/providers claude", "/providers openai base_url=http://localhost:11434/v1", "/providers add local {self} provide openai", "/providers remove local"),
		programVerb(extensionKind, "List the extensions, which give the model tools, show one, set it up, add one, or remove one; what one offers is called after its name",
			"/extensions", "/extensions drive", "/extensions add docs config={\"url\":\"https://example.com/mcp\"} {self} extend mcp", "/extensions drive login", "/extensions remove docs"),
		settingsVerb(),
		{
			name: "rumbos", group: groupHere, args: "[close]", description: "What the rumbo of each session open uses; close ends those neither shown nor working",
			complete: fixed(choice{value: "close", description: "end the rumbos neither shown nor working; their sessions stay"}),
			run:      func(a *app, args []string) tea.Cmd { return a.rumbosCommand(args) },
		},
		{
			name: "help", group: groupHere, args: "[command]", description: "Show the commands and the keys, or what one command does",
			complete: func(a *app, args []string, _ string) func() ([]choice, string) {
				if len(args) > 0 {
					return nil
				}

				choices := commandChoicesOf(a.info.Functions)

				for index := range choices {
					choices[index].value = strings.TrimPrefix(choices[index].value, "/")
				}

				return func() ([]choice, string) { return choices, "" }
			},
			run: func(a *app, args []string) tea.Cmd {
				a.shown.add(&entry{kind: entryAnswer, text: a.help(args)})
				return nil
			},
		},
		{
			name: "quit", group: groupHere, description: "Leave rumbo; a run in progress is cancelled",
			run: func(a *app, _ []string) tea.Cmd { return a.hostCommand("quit", nil) },
		},
	}
}

// wrappedHost are the host's functions the commands are made of, which are
// not commands of their own. A function of the host's that is not among
// them, as one a newer host has, is a command under its name.
var wrappedHost = []string{
	"use", "session-set", "session-values", "status", "skills", "rename", "branch", "branch-points", "compact", "quit", "processes",
	"config", "system-set", "system-values",
	"providers", "provider-models", "provider-show", "provider-set", "provider-values", "provider-add", "provider-rm",
	"extensions", "extension-show", "extension-set", "extension-values", "extension-add", "extension-rm",
}

// isWrapped reports whether a function of the host is one a command is
// made of. Every function of the provider and of an extension is too:
// /login and /logout sign in, and /providers and /extensions call the
// others after the name of what offers them.
func isWrapped(name string) bool {
	return slices.Contains(wrappedHost, name)
}

// verbNamed finds one of the terminal UI's commands by its name.
func verbNamed(name string) (verb, bool) {
	all := verbs()
	index := slices.IndexFunc(all, func(each verb) bool { return each.name == strings.TrimPrefix(name, "/") })

	if index < 0 {
		return verb{}, false
	}

	return all[index], true
}

// fixed completes with choices that do not change, for the first argument.
func fixed(choices ...choice) func(*app, []string, string) func() ([]choice, string) {
	return func(_ *app, args []string, _ string) func() ([]choice, string) {
		if len(args) > 0 {
			return nil
		}

		return func() ([]choice, string) { return choices, "" }
	}
}

// hostCommand calls a function of the host a command is made of, saying so
// when this host does not offer it.
func (a *app) hostCommand(name string, params map[string]string) tea.Cmd {
	index := slices.IndexFunc(a.info.Functions, func(function protocol.Function) bool { return function.Name == name })

	if index < 0 {
		a.shown.add(&entry{kind: entryAnswer, text: "this rumbo cannot " + name + "; it may be older than this terminal UI", failed: true})
		return nil
	}

	var raw json.RawMessage

	if len(params) > 0 {
		raw, _ = json.Marshal(params)
	}

	return a.request(fromHost, a.info.Functions[index], raw)
}

// newSession starts a new session, when this terminal UI starts rumbos.
func (a *app) newSession() tea.Cmd {
	if a.rumbos.start == nil {
		a.shown.add(&entry{kind: entryAnswer, text: "this terminal UI cannot start another rumbo; start rumbo again for a new session", failed: true})
		return nil
	}

	return a.reach("", nil)
}

// listSessionsHere says what sessions the folder has, marked as the
// sidebar marks them, and how to go to one.
func (a *app) listSessionsHere() tea.Cmd {
	if !a.reaches() {
		a.shown.add(&entry{kind: entryAnswer, text: "this terminal UI does not reach the other sessions", failed: true})
		return nil
	}

	if len(a.sessions.listed) == 0 {
		a.shown.add(&entry{kind: entryAnswer, text: "this folder has no session yet; the first task starts one"})
		return a.list()
	}

	shown := a.marked()
	lines := []string{"the sessions of " + homed(a.first().info.Dir) + ", the last used first:", ""}

	for _, listed := range a.sessions.listed {
		mark := " "

		switch {
		case listed.session == shown.current:
			mark = "▌"
		case shown.working[listed.session]:
			mark = "⠋"
		case shown.unseen[listed.session]:
			mark = "•"
		case shown.open[listed.session]:
			mark = "○"
		}

		title := listed.title

		if title == "" {
			title = "(no title)"
		}

		lines = append(lines, fmt.Sprintf("%s %s  %-6s %-11s %s", mark, listed.session, ago(listed.used, a.now), listed.state, render.FirstLine(title)))
	}

	lines = append(lines, "", "▌ shown  ⠋ working  • ended while you were elsewhere  ○ open", "/resume <session> goes to one, /delete <session> deletes one")
	a.shown.add(&entry{kind: entryAnswer, text: strings.Join(lines, "\n")})

	return a.list()
}

// sessionChoices offers the sessions of the folder but the one shown, each
// by its id, with its title and when it was last used; nothing once done,
// when the session is given.
func (a *app) sessionChoices(done bool) func() ([]choice, string) {
	if done {
		return nil
	}

	var choices []choice

	for _, listed := range a.sessions.listed {
		if listed.session == a.session {
			continue
		}

		choices = append(choices, choice{value: listed.session, label: render.FirstLine(listed.title), description: strings.TrimSpace(ago(listed.used, a.now) + " " + listed.state)})
	}

	return func() ([]choice, string) {
		if len(choices) == 0 {
			return nil, "this folder has no other session"
		}

		return choices, ""
	}
}

// sessionNamed finds the session the words name: by its id, the start of
// it, or the start of its title, when only one has it.
func (a *app) sessionNamed(words []string) (string, error) {
	named := strings.Join(words, " ")
	lower := strings.ToLower(named)

	var found []string

	for _, listed := range a.sessions.listed {
		if listed.session == named {
			return named, nil
		}

		if strings.HasPrefix(listed.session, named) || strings.HasPrefix(strings.ToLower(listed.title), lower) {
			found = append(found, listed.session)
		}
	}

	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("no session of this folder is %q; /sessions lists them", named)
	}

	return "", fmt.Errorf("%d sessions start with %q; give more of it, or its id", len(found), named)
}

// resume goes to the session named.
func (a *app) resume(args []string) tea.Cmd {
	session, err := a.sessionNamed(args)

	switch {
	case err != nil:
		a.shown.add(&entry{kind: entryAnswer, text: err.Error(), failed: true})
		return nil
	case session == a.session:
		a.shown.add(&entry{kind: entryAnswer, text: "this is the session shown"})
		return nil
	}

	return a.reach(session, nil)
}

// deleteCommand deletes the session named, or the one shown.
func (a *app) deleteCommand(args []string) tea.Cmd {
	if len(args) == 0 {
		return a.delete()
	}

	session, err := a.sessionNamed(args)

	if err != nil {
		a.shown.add(&entry{kind: entryAnswer, text: err.Error(), failed: true})
		return nil
	}

	return a.deleteSession(session)
}

// messageChoices offers the person's messages a branch can start before,
// by their number, each with what it said.
func (a *app) messageChoices(args []string) func() ([]choice, string) {
	if len(args) > 0 {
		return nil
	}

	var choices []choice

	for index, each := range a.shown.entries {
		if point := a.shown.point(index); point > 0 {
			choices = append(choices, choice{value: strconv.Itoa(point), label: "“" + cut(render.FirstLine(strings.TrimSpace(each.text)), 60) + "”"})
		}
	}

	return func() ([]choice, string) {
		return choices, "[message]  the branch starts before it, with it in the composer; without one, from now"
	}
}

// branchCommand branches the session before the message numbered, or from
// now.
func (a *app) branchCommand(args []string) tea.Cmd {
	if !a.branches() {
		a.shown.add(&entry{kind: entryAnswer, text: "this session cannot be branched: it has no run yet, or its host does not branch", failed: true})
		return nil
	}

	if len(args) == 0 {
		return a.branchAt(0, "", nil, false)
	}

	point, err := strconv.Atoi(args[0])

	for index, each := range a.shown.entries {
		if err == nil && a.shown.point(index) == point {
			return a.branchAt(point, each.text, each.images, false)
		}
	}

	a.shown.add(&entry{kind: entryAnswer, text: "there is no message " + args[0] + " of yours here; Tab after /branch lists them", failed: true})

	return nil
}

// showPanel shows a panel, or reads again the one shown.
func (a *app) showPanel(kind int) tea.Cmd {
	if a.panels.shown == kind {
		return a.readPanel()
	}

	return a.togglePanel(kind)
}

// rumbosCommand says what the rumbos use, or closes those not in use.
func (a *app) rumbosCommand(args []string) tea.Cmd {
	switch strings.Join(args, " ") {
	case "":
		return a.usage()
	case "close":
		a.shown.add(&entry{kind: entryAnswer, text: "closing the rumbos neither shown nor working; their sessions stay"})
		return a.tidy(0)
	}

	a.shown.add(&entry{kind: entryAnswer, text: "usage: /rumbos [close]", failed: true})

	return nil
}

// modelChoices offers, for the word being typed after /model, the
// providers, the models of the one before the slash, and the options of
// the session's provider with their values.
func (a *app) modelChoices(args []string, stem string) func() ([]choice, string) {
	provider := a.info.Provider
	modelGiven := slices.ContainsFunc(args, func(word string) bool { return !strings.Contains(word, "=") })

	if named, found := strings.CutSuffix(stem, "/"); found && !modelGiven {
		models := a.hostChoices("provider-models", map[string]string{"provider": named}, "")

		return func() ([]choice, string) {
			choices, hint := models()

			for index := range choices {
				choices[index].value = named + "/" + choices[index].value
			}

			return choices, hint
		}
	}

	providers, options := a.hostChoices("providers", nil, ""), a.hostChoices("session-values", nil, "")

	return func() ([]choice, string) {
		var choices []choice

		if !modelGiven {
			listed, _ := providers()

			for _, each := range listed {
				said := "its models"

				if each.value == provider {
					said = "the session's provider; its models"
				}

				choices = append(choices, choice{value: each.value + "/", description: said})
			}
		}

		// The options are those of the session's provider.
		offered, _ := options()

		return append(choices, offered...), ""
	}
}

// modelCommand opens the picker, or changes the model and the options the
// words say: a provider/model, or a model of the session's provider, and
// option=value changes, in order.
func (a *app) modelCommand(args []string) tea.Cmd {
	if len(args) == 0 {
		return a.openPicker()
	}

	if !a.offers("use") {
		a.shown.add(&entry{kind: entryAnswer, text: "this rumbo cannot change the model", failed: true})
		return nil
	}

	var model string
	var options []string

	for _, word := range args {
		switch {
		case strings.Contains(word, "="):
			options = append(options, word)
		case model == "":
			model = word
		default:
			a.shown.add(&entry{kind: entryAnswer, text: "usage: /model [provider/model] [option=value…]", failed: true})
			return nil
		}
	}

	a.asking = true

	held, conv, label := a.host, a.shown, a.label(fromHost)
	conn, provider, listsProviders := held.conn, a.info.Provider, a.offers("providers")

	return func() tea.Msg {
		var said []string
		var state *protocol.HostState

		call := func(name string, params any) error {
			raw, _ := json.Marshal(params)
			result, err := conn.InvokeHost(name, raw)

			if err != nil {
				return err
			}

			said = append(said, strings.TrimSpace(result.Text))
			state = &result.State

			return nil
		}

		if model != "" {
			// What comes before the slash is a provider when there is one of
			// that name; a model's own name may have slashes too.
			using, chosen := provider, model

			if named, rest, found := strings.Cut(model, "/"); found && listsProviders {
				if listed, err := conn.InvokeHost("providers", nil); err == nil && slices.ContainsFunc(listed.Values, func(value protocol.Value) bool { return value.Value == named }) {
					using, chosen = named, rest
				}
			}

			params := map[string]string{"provider": using}

			if chosen != "" {
				params["model"] = chosen
			}

			if err := call("use", params); err != nil {
				return answeredMsg{host: held, conv: conv, label: label, err: err}
			}
		}

		for _, option := range options {
			if err := call("session-set", map[string]string{"change": option}); err != nil {
				return answeredMsg{host: held, conv: conv, label: label, text: strings.Join(said, "\n"), err: err, state: state}
			}
		}

		return answeredMsg{host: held, conv: conv, label: label, text: strings.Join(said, "\n"), state: state}
	}
}

// signing finds a sign-in function: the provider's, or that of the
// extension named, with the source it is called on.
func (a *app) signing(name string, args []string) (protocol.Function, string, string) {
	if len(args) == 0 {
		index := slices.IndexFunc(a.described.Offers.Functions, func(function protocol.Function) bool { return function.Name == name })

		if index < 0 {
			return protocol.Function{}, "", a.info.Provider + " has no " + name + "; it is set up with /providers"
		}

		return a.described.Offers.Functions[index], fromProvider, ""
	}

	for _, extension := range a.described.Extensions {
		if extension.Name != args[0] {
			continue
		}

		for _, function := range extension.Functions {
			if function.Name == name {
				// An extension's function is called by its command's name.
				function.Name = extension.Name + "-" + function.Name

				return function, fromExtension(extension.Name), ""
			}
		}

		return protocol.Function{}, "", "the extension " + extension.Name + " has no " + name
	}

	return protocol.Function{}, "", "there is no extension " + args[0] + " here"
}

// sign calls the provider's sign-in, or an extension's.
func (a *app) sign(name string, args []string) tea.Cmd {
	if a.described == nil {
		a.asking = true
		return a.describe(a.host, strings.TrimSpace("/"+name+" "+strings.Join(args, " ")))
	}

	function, source, why := a.signing(name, args)

	if why != "" {
		a.shown.add(&entry{kind: entryAnswer, text: why, failed: true})
		return nil
	}

	return a.request(source, function, nil)
}

// signChoices offers the extensions that have the sign-in function name.
func (a *app) signChoices(name string, args []string) func() ([]choice, string) {
	if len(args) > 0 {
		return nil
	}

	described, provider := a.described, a.info.Provider
	conn := a.conn

	return func() ([]choice, string) {
		if described == nil {
			told, err := conn.Describe()

			if err != nil {
				return nil, err.Error()
			}

			described = &told
		}

		var choices []choice

		for _, extension := range described.Extensions {
			for _, function := range extension.Functions {
				if function.Name == name {
					choices = append(choices, choice{value: extension.Name, description: function.Description})
				}
			}
		}

		return choices, "without one, " + name + " of the provider " + provider
	}
}
