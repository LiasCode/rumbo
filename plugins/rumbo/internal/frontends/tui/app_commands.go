package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/LiasCode/rumbo/protocol"
)

// A slash opens a menu of what the host, the provider, and the terminal UI
// offer, built from what they describe, and what a command answers shows
// in the conversation where it was typed.

// enter sends what is typed: a command, or a task, which goes to the run in
// progress when one works. With the menu open on a choice
// that is not typed yet, it takes the choice first, and sends the line
// only when the command needs nothing more.
func (a *app) enter() tea.Cmd {
	if a.menu != nil && len(a.menu.shown) > 0 {
		_, token := splitLast(a.input.String())

		if chosen, ok := a.menu.current(); ok && chosen.value != token {
			a.take()

			// A name= waits for its value, or for Enter again to send it
			// empty.
			if lacksParams(a.info.Functions, a.input.String()) || strings.HasSuffix(chosen.value, "=") || strings.HasSuffix(chosen.value, "/") || chosen.continues {
				return a.refreshMenu()
			}
		}
	}

	// A command that still needs something opens the menu of what it takes
	// next, rather than failing for the want of it.
	if typed := a.input.String(); isCommand(strings.TrimSpace(typed)) && lacksParams(a.info.Functions, typed) {
		if !strings.HasSuffix(typed, " ") {
			a.input.set(typed + " ")
		}

		a.dismissed = false

		return a.refreshMenu()
	}

	line, files := strings.TrimSpace(a.input.String()), a.files
	said := line != "" || len(files) > 0

	if line != "" {
		a.history.add(line)
	}

	a.menu = nil

	switch run := a.here(); {
	case isCommand(line) && a.asking:
		a.notice = "a command is waiting for its answer"
	case isCommand(line):
		a.input.clear()
		return a.command(line)
	case said && run != nil:
		return a.steer(run, line, files)
	case said:
		return a.submit(line, files)
	}

	return nil
}

// take puts the choice selected in the menu in place of the word being
// typed. A name= is taken to go on with its value.
func (a *app) take() {
	chosen, ok := a.menu.current()

	if !ok {
		return
	}

	head, _ := splitLast(a.input.String())

	// A name= goes on with its value, a provider/ with its models, and an
	// item of a list with a comma for another, or with Enter.
	if strings.HasSuffix(chosen.value, "=") || strings.HasSuffix(chosen.value, "/") || chosen.continues {
		a.input.set(head + chosen.value)
		return
	}

	a.input.set(head + chosen.value + " ")
}

// command shows a command line in the conversation and runs it.
func (a *app) command(line string) tea.Cmd {
	if a.asking {
		a.notice = "a command is waiting for its answer"
		return nil
	}

	a.shown.add(&entry{kind: entryTyped, text: line})
	a.shown.follow = true

	return a.execute(line)
}

// execute runs a command: one of the terminal UI's own, or a function of
// the host, of the provider, or of an extension; what is not the host's has
// to be said first by the host, which asks them.
func (a *app) execute(line string) tea.Cmd {
	words := strings.Fields(line)
	name, args := strings.TrimPrefix(words[0], "/"), words[1:]

	if each, found := verbNamed(name); found {
		if each.needs() && len(args) == 0 {
			a.shown.add(&entry{kind: entryAnswer, text: "usage: " + verbUsage(each), failed: true})
			return nil
		}

		return each.run(a, args)
	}

	if function, _, found := lookup(words[0], a.info.Functions); found {
		return a.call(fromHost, function, args)
	}

	said := fmt.Sprintf("there is no command %s; /help lists them", words[0])

	if near := closest(words[0], commandChoicesOf(a.info.Functions)); near != "" {
		said = fmt.Sprintf("there is no command %s; did you mean %s?", words[0], near)
	}

	a.shown.add(&entry{kind: entryAnswer, text: said, failed: true})

	return nil
}

// call runs a function of the host or of the provider with the words typed
// after it. One that asks to be confirmed waits for the person to agree.
func (a *app) call(source string, function protocol.Function, args []string) tea.Cmd {
	params, err := arguments(function, args)

	if err != nil {
		a.shown.add(&entry{kind: entryAnswer, label: a.label(source), text: err.Error(), failed: true})
		return nil
	}

	return a.request(source, function, params)
}

// invoke calls a function of the host, of the provider, or of an
// extension, whose answer shows in conv. What a function of the last two
// says and asks while it works shows there too.
func (a *app) invoke(source, name string, params json.RawMessage, conv *conversation) tea.Cmd {
	held, send, label := a.host, a.send, a.label(source)

	// Quitting is of the terminal UI, which ends with the rumbo that
	// started it, whichever session shows.
	if source == fromHost && name == "quit" {
		held = a.first()
	}

	conn := held.conn

	if source == fromHost {
		// What the extensions offer may change with a function on them,
		// such as one registered, and is asked again before it is needed.
		index := slices.IndexFunc(a.info.Functions, func(function protocol.Function) bool { return function.Name == name })
		stale := index >= 0 && a.info.Functions[index].Group == protocol.GroupExtension
		follows := name == "branch" && a.rumbos.start != nil

		return func() tea.Msg {
			result, err := conn.InvokeHost(name, params)

			if err != nil {
				return answeredMsg{host: held, conv: conv, label: label, err: err, stale: stale}
			}

			answered := answeredMsg{host: held, conv: conv, label: label, text: strings.TrimSpace(result.Text + "\n" + list(result.Values)), state: &result.State, stale: stale}

			// A branch is another session, which shows next.
			if follows && len(result.Values) == 1 {
				answered.opens = result.Values[0].Value
			}

			return answered
		}
	}

	talking := talk{send: send, conv: conv, label: label, who: "the provider"}
	extension, extended := extensionOf(source)

	// An extension's command is named after it, and its function is not.
	if extended {
		talking.who, name = "the extension "+extension, strings.TrimPrefix(name, extension+"-")
	}

	return func() tea.Msg {
		named := func(id int64) { send(invokingMsg{id}) }

		var raw json.RawMessage
		var err error

		if extended {
			raw, err = conn.InvokeExtension(extension, name, params, named, talking)
		} else {
			raw, err = conn.InvokeWith(name, params, named, talking)
		}

		if err != nil {
			return answeredMsg{host: held, conv: conv, label: label, err: err, stale: extended}
		}

		return answeredMsg{host: held, conv: conv, label: label, text: answerText(raw), stale: extended}
	}
}

// answerText is what a provider's function answered, for a person: the
// values it lists, its words when it answers in words, or its JSON.
func answerText(raw json.RawMessage) string {
	var listed protocol.ValuesResult

	if json.Unmarshal(raw, &listed) == nil && len(listed.Values) > 0 {
		return list(listed.Values)
	}

	var text string

	if json.Unmarshal(raw, &text) == nil {
		return text
	}

	return string(raw)
}

// answered shows what a function answered, and takes in what the host says
// the front end is attached to after it. A branch it made shows next.
func (a *app) answered(msg answeredMsg) tea.Cmd {
	a.asking = false
	a.invoking = 0

	// What is offered is asked again before the next command that needs it.
	if msg.stale {
		msg.host.described = nil
	}

	// What was done before a failure, as when the picker changes several
	// things, shows before it.
	if msg.text != "" {
		msg.conv.add(&entry{kind: entryAnswer, label: msg.label, text: msg.text})
	}

	if msg.err != nil {
		msg.conv.add(&entry{kind: entryAnswer, label: msg.label, text: msg.err.Error(), failed: true})
	}

	if msg.state == nil {
		return nil
	}

	// The command may have changed what the panel shows, and what the
	// session is called.
	cmds := []tea.Cmd{a.adopt(msg.host, *msg.state), a.readPanel(), a.list()}

	if msg.err == nil && msg.opens != "" {
		cmds = append(cmds, a.reach(msg.opens, msg.then))
	}

	return tea.Batch(cmds...)
}

// help lists the commands and the keys, or says what one command does.
func (a *app) help(args []string) string {
	if len(args) > 0 {
		return a.helpOf(strings.TrimPrefix(args[0], "/"))
	}

	var out strings.Builder

	for index, group := range []string{groupSessions, groupModel, groupSetUp, groupHere} {
		if index > 0 {
			out.WriteString("\n")
		}

		out.WriteString(group + "\n")

		for _, each := range verbs() {
			if each.group == group {
				fmt.Fprintf(&out, "  %s\n", verbUsage(each))
			}
		}
	}

	// What no command wraps yet is a command under its own name.
	var more []string

	for _, function := range commands(a.info.Functions) {
		if !isWrapped(function.Name) {
			more = append(more, usage(function))
		}
	}

	if len(more) > 0 {
		out.WriteString("\nmore, of this rumbo's host\n")

		for _, each := range more {
			out.WriteString("  " + each + "\n")
		}
	}

	out.WriteString("\n/help <command> says more of one, with examples. A slash on an empty line opens the menu of the commands, as Ctrl-P does: Up and Down choose, Tab or Enter take, Escape closes it; it goes on with what the command takes next, and what is still to type shows after the cursor.\n")
	out.WriteString("Enter sends; Shift-Enter, Alt-Enter, or Ctrl-J starts a new line. Up and Down go through what you entered before, which Ctrl-R searches. Ctrl-V adds the files or the image in the clipboard to what you send next, as does dropping a file on the terminal; /attach adds one by its path.\n")
	out.WriteString("Escape cancels the run; Ctrl-C does too, else clears what is typed, else quits.\n")
	out.WriteString("Shift-Tab goes up into the conversation, where j and k, or Up and Down, choose, K and J go to your messages, and gg and G to either end. Enter on a tool call shows all of it, on what you asked offers to change it or ask it again in a branch, and on anything else copies it; c copies what is chosen, and b branches from the message of yours its task started with, once you agree.\n")
	out.WriteString("Each session is a rumbo of its own. With the sessions shown (Ctrl-B), Tab on an empty line goes to them, where j, k, gg, and G choose, Enter shows one, starting its rumbo, and d deletes one, once you agree; Ctrl-N starts a new one. The runs of the sessions you leave go on.\n")
	out.WriteString("Ctrl-Alt-B shows the session beside the conversation, as /status does, and Ctrl-G what changed in the folder, as /changes does.\n")
	out.WriteString("A click on the model under the composer chooses the model and the options, as /model does. PgUp and PgDn, or the mouse's wheel, scroll the conversation.\n")
	out.WriteString("Dragging the mouse over the conversation or the panel selects its text, which is copied as you let go; with Shift, the terminal selects as it does elsewhere.\n")
	out.WriteString("Anything that is not a command is a task.")

	return out.String()
}

// helpOf says what one command does, how it is written, and examples.
func (a *app) helpOf(name string) string {
	if each, found := verbNamed(name); found {
		text := verbUsage(each)

		if len(each.examples) > 0 {
			text += "\n\nfor example:\n  " + strings.Join(each.examples, "\n  ")
		}

		return text
	}

	if function, _, found := lookup("/"+name, a.info.Functions); found {
		return usage(function)
	}

	return "there is no command /" + name + "; /help lists them"
}

// verbUsage is how a command is written, and what it does.
func verbUsage(each verb) string {
	return fmt.Sprintf("%-40s %s", strings.TrimSpace("/"+each.name+" "+each.args), each.description)
}

// copyReply puts the model's last answer in the session shown, or only its
// last code block, in the clipboard.
func (a *app) copyReply(args []string) tea.Cmd {
	text := a.shown.reply

	fail := func(why string) tea.Cmd {
		a.shown.add(&entry{kind: entryAnswer, text: why, failed: true})
		return nil
	}

	if strings.TrimSpace(text) == "" {
		return fail("there is no answer to copy yet")
	}

	switch strings.Join(args, " ") {
	case "":
	case "code":
		code, found := lastCode(text)

		if !found {
			return fail("the last answer has no code block; /copy takes all of it")
		}

		text = code
	default:
		return fail("usage: /copy [code]")
	}

	return a.copy(text)
}

// export writes what the session shown holds as Markdown: to the path
// given, which is taken from the session's directory, or to
// rumbo-<session>.md there. It never writes over a file that exists.
func (a *app) export(args []string) tea.Cmd {
	session, dir, conv := a.session, a.info.Dir, a.shown

	if session == "" {
		a.shown.add(&entry{kind: entryAnswer, text: "this session holds nothing yet", failed: true})
		return nil
	}

	path := filepath.Join(dir, "rumbo-"+session+".md")

	if len(args) > 0 {
		path = strings.Join(args, " ")

		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
	}

	a.asking = true
	held := a.host

	return func() tea.Msg {
		told, err := held.conn.History()

		if err == nil {
			err = writeNew(path, markdownOf(told, time.Now()))
		}

		if err != nil {
			return answeredMsg{host: held, conv: conv, err: err}
		}

		return answeredMsg{host: held, conv: conv, text: "wrote the session to " + path}
	}
}

// refreshMenu works out the menu for what is typed: commands while the
// first word is, and then the values of the parameter the word being typed
// is for, which may have to be asked for.
func (a *app) refreshMenu() tea.Cmd {
	line := a.input.String()

	if a.dismissed || a.searching || !isCommandStart(line) {
		a.menu = nil
		return nil
	}

	head, token := splitLast(line)

	head += stemOf(line, token)

	// The selection stays where Up and Down took it until the word changes.
	if a.menu != nil && a.menu.head == head {
		if !a.menu.loading && a.menu.token != token {
			a.menu.filter(token)
		}

		return nil
	}

	asked := &menu{head: head}
	a.menu = asked

	if head == "" {
		asked.choices = commandChoicesOf(a.info.Functions)
		asked.filter(token)

		return nil
	}

	asked.loading = true

	return a.offer(asked)
}

// offer asks what the word after a menu's head can be, with what describes
// each, or a hint saying what it is or why there is nothing to offer.
func (a *app) offer(asked *menu) tea.Cmd {
	conn, host := a.conn, a.info.Functions
	words := strings.Fields(asked.head)

	// The terminal UI's own commands work out their choices themselves.
	if each, found := verbNamed(words[0]); found {
		args, stem := words[1:], ""

		if !strings.HasSuffix(asked.head, " ") && len(args) > 0 {
			args, stem = args[:len(args)-1], args[len(args)-1]
		}

		var work func() ([]choice, string)

		if each.complete != nil {
			work = each.complete(a, args, stem)
		}

		return func() tea.Msg {
			if work == nil {
				return choicesMsg{menu: asked}
			}

			choices, hint := work()

			return choicesMsg{asked, choices, hint}
		}
	}

	function, target, found := lookup(words[0], host)

	if !found {
		return func() tea.Msg { return choicesMsg{menu: asked} }
	}

	return func() tea.Msg {

		given, pending := typedParams(function, words[1:])

		if len(pending) == 0 {
			return choicesMsg{menu: asked}
		}

		param := pending[0]
		var params json.RawMessage

		if param.Values != "" && param.Type != protocol.OptionBoolean {
			params = passedTo(host, param.Values, given)
		}

		choices, hint := listValues(conn, param, target, params)

		// What takes any value says what it is.
		if len(choices) == 0 && hint == "" {
			hint = strings.TrimSpace(signature(param) + "  " + param.Description)
		}

		return choicesMsg{asked, choices, hint}
	}
}

// stemOf is the part of the word being typed after which the choices are
// asked for again: past the first slash of a provider/model, as the models
// of the provider before it, since a model's name may hold more; and, in
// a command of the terminal UI's, past a name= and each comma after it, as
// the items of a list.
func stemOf(line, token string) string {
	cut := 0

	if slash := strings.IndexByte(token, '/'); slash > 0 && !strings.Contains(token[:slash], "=") {
		cut = slash + 1
	}

	if _, own := verbNamed(strings.Fields(line)[0]); own && strings.Contains(token, "=") {
		cut = max(cut, strings.LastIndexAny(token, "=,")+1)
	}

	return token[:cut]
}

// lookup finds the function of the host a command names, among those no
// command of the terminal UI's wraps.
func lookup(command string, host []protocol.Function) (protocol.Function, string, bool) {
	name := strings.TrimPrefix(command, "/")

	if index := slices.IndexFunc(host, func(function protocol.Function) bool { return function.Name == name }); index >= 0 && !isWrapped(name) {
		return host[index], protocol.TargetHost, true
	}

	return protocol.Function{}, "", false
}

// offered shows the choices asked for a menu, if the menu is still open.
func (a *app) offered(msg choicesMsg) {
	if a.menu != msg.menu {
		return
	}

	_, token := splitLast(a.input.String())

	msg.menu.loading, msg.menu.choices, msg.menu.hint = false, msg.choices, msg.hint
	msg.menu.filter(token)
}

// pasteClipboard puts the files in the clipboard in the composer, or its
// image, saved as a file, in the background, since the clipboard may take a
// moment.
func (a *app) pasteClipboard() tea.Cmd {
	read := a.clipboard

	if read == nil {
		read = readClipboard
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), clipboardTimeout)
		defer cancel()

		found, err := read(ctx)

		if err != nil || len(found.files) > 0 {
			return pastedMsg{found.files, err}
		}

		file, err := keep(found.image)

		if err != nil {
			return pastedMsg{err: err}
		}

		return pastedMsg{files: []string{file}}
	}
}

// talk is the Dialog of a function of the provider or of an extension:
// what it says shows in the
// conversation it was called from, and what it asks opens over everything,
// with a line of its own for the answer.
type talk struct {
	send  func(tea.Msg)
	conv  *conversation
	label string
	// who asks, in words.
	who string
}

func (t talk) Say(text string) {
	t.send(saidMsg{t.conv, t.label, text})
}

func (t talk) Ask(ctx context.Context, text string, secret bool) (string, error) {
	asked := &question{secret: secret, answer: make(chan string, 1)}
	t.send(askedMsg{t.conv, t.label, asked, text, t.who})

	select {
	case answer := <-asked.answer:
		return answer, nil

	case <-ctx.Done():
		t.send(askGoneMsg{asked})

		return "", ctx.Err()
	}
}
