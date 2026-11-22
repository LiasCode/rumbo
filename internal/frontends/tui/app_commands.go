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

	"github.com/LiasCode/rumbo/internal/frontends/render"
	"github.com/LiasCode/rumbo/protocol"
)

// A slash opens a menu of what the host, the provider, and the terminal UI
// offer, built from what they describe, and what a command answers shows
// in the conversation where it was typed.

// enter sends what is typed: a command, or a task, which goes to the run in
// the session shown when one works there. With the menu open on a choice
// that is not typed yet, it takes the choice first, and sends the line
// only when the command needs nothing more.
func (a *app) enter() tea.Cmd {
	if a.menu != nil && len(a.menu.shown) > 0 {
		_, token := splitLast(a.input.String())

		if chosen, ok := a.menu.current(); ok && chosen.value != token {
			a.take()

			// A name= waits for its value, or for Enter again to send it
			// empty.
			if lacksParams(a.info.Functions, a.described, a.input.String()) || strings.HasSuffix(chosen.value, "=") {
				return a.refreshMenu()
			}
		}
	}

	// A command that still needs something opens the menu of what it takes
	// next, rather than failing for the want of it.
	if typed := a.input.String(); isCommand(strings.TrimSpace(typed)) && lacksParams(a.info.Functions, a.described, typed) {
		if !strings.HasSuffix(typed, " ") {
			a.input.set(typed + " ")
		}

		a.dismissed = false

		return a.refreshMenu()
	}

	line, images := strings.TrimSpace(a.input.String()), a.pasted
	said := line != "" || len(images) > 0

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
		return a.steer(run, line, images)
	case said:
		return a.submit(line, images)
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

	if strings.HasSuffix(chosen.value, "=") {
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
// the host or of the provider, which has to say what it offers first.
func (a *app) execute(line string) tea.Cmd {
	words := strings.Fields(line)
	name, args := strings.TrimPrefix(words[0], "/"), words[1:]

	switch words[0] {
	case commandHelp:
		a.shown.add(&entry{kind: entryAnswer, text: a.help()})
		return nil
	case commandCopy:
		return a.copyReply(args)
	case commandExport:
		return a.export(args)
	}

	// The host's functions come first: they need no provider to be running.
	if index := slices.IndexFunc(a.info.Functions, func(function protocol.Function) bool { return function.Name == name }); index >= 0 {
		return a.call(fromHost, a.info.Functions[index], args)
	}

	if a.described == nil {
		a.asking = true
		return a.describe(line)
	}

	if index := slices.IndexFunc(a.described.Offers.Functions, func(function protocol.Function) bool { return function.Name == name }); index >= 0 {
		return a.call(fromProvider, a.described.Offers.Functions[index], args)
	}

	a.shown.add(&entry{kind: entryAnswer, text: fmt.Sprintf("there is no command %s; see %s", words[0], commandHelp), failed: true})

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

// invoke calls a function of the host or of the provider, whose answer
// shows in conv unless quiet. What a provider's function says and asks
// while it works shows there too.
func (a *app) invoke(source, name string, params json.RawMessage, conv *conversation, quiet bool) tea.Cmd {
	conn, send, label := a.conn, a.send, a.label(source)

	if source == fromHost {
		return func() tea.Msg {
			result, err := conn.InvokeHost(name, params)

			if err != nil {
				return answeredMsg{conv: conv, label: label, err: err, quiet: quiet}
			}

			return answeredMsg{conv: conv, label: label, text: strings.TrimSpace(result.Text + "\n" + list(result.Values)), state: &result.State, quiet: quiet}
		}
	}

	talking := talk{send: send, conv: conv, label: label}

	return func() tea.Msg {
		raw, err := conn.InvokeWith(name, params, func(id int64) { send(invokingMsg{id}) }, talking)

		if err != nil {
			return answeredMsg{conv: conv, label: label, err: err, quiet: quiet}
		}

		return answeredMsg{conv: conv, label: label, text: answerText(raw), quiet: quiet}
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
// the front end is attached to after it. What the sidebar asked says only
// what went wrong.
func (a *app) answered(msg answeredMsg) tea.Cmd {
	a.asking = false
	a.invoking = 0

	// What was done before a failure, as when the picker changes several
	// things, shows before it.
	if msg.text != "" && !msg.quiet {
		msg.conv.add(&entry{kind: entryAnswer, label: msg.label, text: msg.text})
	}

	switch {
	case msg.err != nil && msg.quiet:
		a.notice = msg.err.Error()
	case msg.err != nil:
		msg.conv.add(&entry{kind: entryAnswer, label: msg.label, text: msg.err.Error(), failed: true})
	}

	if msg.state == nil {
		return nil
	}

	// What a command answered in a session left behind is said here too.
	if msg.state.Session != a.session && !msg.quiet && msg.text != "" {
		a.notice = render.FirstLine(msg.text)
	}

	// The panel shows the session shown, which a session left behind reads
	// for itself.
	cmds := []tea.Cmd{a.list()}

	if msg.state.Session == a.session {
		cmds = append(cmds, a.readPanel())
	}

	cmds = append(cmds, a.adopt(*msg.state))

	if msg.then != nil && msg.err == nil {
		cmds = append(cmds, msg.then())
	}

	return tea.Batch(cmds...)
}

// help lists every command there is, grouped by what it acts on, and the
// keys.
func (a *app) help() string {
	var out strings.Builder

	fmt.Fprintf(&out, "%s  rumbo itself, the same with any provider. Its commands, by what they act on:\n", a.label(fromHost))

	// The groups come in the order the host lists its functions.
	var groups []string

	members := map[string][]protocol.Function{}

	for _, function := range commands(a.info.Functions) {
		if _, seen := members[function.Group]; !seen {
			groups = append(groups, function.Group)
		}

		members[function.Group] = append(members[function.Group], function)
	}

	for _, group := range groups {
		fmt.Fprintf(&out, "\n%s\n", groupTitle(group))

		for _, function := range members[group] {
			fmt.Fprintf(&out, "%s\n", usage(function))
		}
	}

	fmt.Fprintf(&out, "\n%s  what reaches the model. What it offers changes with the provider; /provider-show shows its settings.\n", a.label(fromProvider))

	if a.described == nil {
		out.WriteString("It has not said yet what it offers.\n")
	} else {
		for _, function := range commands(a.described.Offers.Functions) {
			fmt.Fprintf(&out, "%s\n", usage(function))
		}
	}

	out.WriteString("\nthe terminal UI's own\n")

	for _, function := range ownCommands {
		fmt.Fprintf(&out, "%s\n", usage(function))
	}

	out.WriteString("\nEnter sends; Shift-Enter, Alt-Enter, or Ctrl-J starts a new line. Ctrl-P opens the commands, which typing narrows; one that takes something asks for it in a form, and one that cannot be undone asks you to agree first.\n")
	out.WriteString("A slash in the composer opens the menu of the commands too: Up and Down choose, Tab or Enter take, Escape closes it.\n")
	out.WriteString("Up and Down go through what you entered before, which Ctrl-R searches. Ctrl-V adds the image in the clipboard to what you send next.\n")
	out.WriteString("Escape cancels the run of the session shown; Ctrl-C does too, else clears what is typed, else quits.\n")
	out.WriteString("Tab on an empty composer goes to the sessions, where Enter shows one and Delete deletes it; Ctrl-N starts a new session, and Ctrl-B shows or puts away the sidebar.\n")
	out.WriteString("Shift-Tab goes up into the conversation: Enter on a tool call shows all of it, on your message changes it or asks it again in a branch, and on anything else copies it.\n")
	out.WriteString("Ctrl-Alt-B shows the session's details beside the conversation, and Ctrl-G what changed in the project's folder, as Git sees it.\n")
	out.WriteString("A click on the model under the composer chooses the model and the options. PgUp and PgDn, or the mouse's wheel, scroll the conversation.\n")
	out.WriteString("Dragging the mouse over the conversation or the panel selects its text, which is copied as you let go; with Shift, the terminal selects as it does elsewhere.\n")
	out.WriteString("Anything that is not a command is a task.")

	return out.String()
}

// copyReply puts the model's last answer in the session shown, or only its
// last code block, in the clipboard, through the terminal (OSC 52), so it
// works over ssh as well.
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

	text = strings.TrimRight(text, "\n")
	lines := strings.Count(text, "\n") + 1
	a.shown.add(&entry{kind: entryAnswer, text: fmt.Sprintf("sent %d line(s) to the clipboard, in a terminal that takes OSC 52; in tmux, with set-clipboard on", lines)})

	return tea.SetClipboard(text)
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
	conn := a.conn

	return func() tea.Msg {
		told, err := conn.History(session)

		if err == nil {
			err = writeNew(path, markdownOf(told, time.Now()))
		}

		if err != nil {
			return answeredMsg{conv: conv, err: err}
		}

		return answeredMsg{conv: conv, text: "wrote the session to " + path}
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
		asked.choices = commandChoicesOf(a.info.Functions, a.described)
		asked.filter(token)

		return nil
	}

	asked.loading = true

	return a.offer(asked)
}

// offer asks what the word after a menu's head can be, with what describes
// each, or a hint saying what it is or why there is nothing to offer.
func (a *app) offer(asked *menu) tea.Cmd {
	conn, host, described := a.conn, a.info.Functions, a.described
	words := strings.Fields(asked.head)

	return func() tea.Msg {
		function, target, found := lookup(words[0], host, described)

		if !found && described == nil {
			if told, err := conn.Describe(); err == nil {
				described = &told
				function, target, found = lookup(words[0], host, described)
			}
		}

		position := len(words) - 1

		if !found || position >= len(function.Params) {
			return choicesMsg{menu: asked}
		}

		given := map[string]string{}

		for index, param := range function.Params[:position] {
			given[param.Name] = words[index+1]
		}

		param := function.Params[position]
		functions := host

		if target == protocol.TargetProvider && described != nil {
			functions = described.Offers.Functions
		}

		var params json.RawMessage

		if param.Values != "" && param.Type != protocol.OptionBoolean {
			params = passedTo(functions, param.Values, given)
		}

		choices, hint := listValues(conn, param, target, params)

		// What takes any value says what it is.
		if len(choices) == 0 && hint == "" {
			hint = strings.TrimSpace(signature(param) + "  " + param.Description)
		}

		return choicesMsg{asked, choices, hint}
	}
}

// lookup finds what a command names: one of the terminal UI's own, a
// function of the host, or one the provider described, and whose it is.
func lookup(command string, host []protocol.Function, described *protocol.DescribeResult) (protocol.Function, string, bool) {
	name := strings.TrimPrefix(command, "/")
	named := func(function protocol.Function) bool { return function.Name == name }

	if index := slices.IndexFunc(ownCommands, named); index >= 0 {
		return ownCommands[index], fromHere, true
	}

	if index := slices.IndexFunc(host, named); index >= 0 {
		return host[index], protocol.TargetHost, true
	}

	if described != nil {
		if index := slices.IndexFunc(described.Offers.Functions, named); index >= 0 {
			return described.Offers.Functions[index], protocol.TargetProvider, true
		}
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

// pasteImage reads the image in the clipboard into the composer, in the
// background, since the clipboard may take a moment.
func (a *app) pasteImage() tea.Cmd {
	read := a.clipboard

	if read == nil {
		read = readClipboard
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), clipboardTimeout)
		defer cancel()

		data, err := read(ctx)

		if err != nil {
			return pastedMsg{err: err}
		}

		shown, err := prepared(data)

		return pastedMsg{shown, err}
	}
}

// talk is the Dialog of a provider's function: what it says shows in the
// conversation it was called from, and what it asks opens over everything,
// with a line of its own for the answer.
type talk struct {
	send  func(tea.Msg)
	conv  *conversation
	label string
}

func (t talk) Say(text string) {
	t.send(saidMsg{t.conv, t.label, text})
}

func (t talk) Ask(ctx context.Context, text string, secret bool) (string, error) {
	asked := &question{secret: secret, answer: make(chan string, 1)}
	t.send(askedMsg{t.conv, t.label, asked, text})

	select {
	case answer := <-asked.answer:
		return answer, nil

	case <-ctx.Done():
		t.send(askGoneMsg{asked})

		return "", ctx.Err()
	}
}
