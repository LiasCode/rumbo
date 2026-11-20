package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/LiasCode/rumbo/internal/frontends/render"
	"github.com/LiasCode/rumbo/protocol"
)

// A line that starts with a slash and a word is a command, not a task. A
// few commands are the front end's own. Every other one is a function the
// host or the provider described: the front end knows none of them
// beforehand, and gains a command whenever either of them gains a function.
const (
	commandHelp   = "/help"
	commandCopy   = "/copy"
	commandExport = "/export"
)

// ownCommands are the front end's own, described as functions are, so they
// are completed and listed the same way. They come before a function of the
// host or the provider of the same name.
var ownCommands = []protocol.Function{
	{Name: "help", Description: "Show every command"},
	{
		Name:        "copy",
		Description: "Put the model's last answer in the clipboard, or only its last code block",
		Params:      []protocol.Option{{Name: "what", Type: protocol.OptionString, Enum: []string{"code"}, Optional: true, Description: "code: only its last code block"}},
	},
	{
		Name:        "export",
		Description: "Write this session as Markdown to a file, which it does not overwrite",
		Params:      []protocol.Option{{Name: "path", Type: protocol.OptionString, Optional: true, Description: "without it, rumbo-<session>.md in the session's directory"}},
	},
}

// Where a command acts and where an answer comes from. The host is rumbo
// itself and is the same with any provider; the provider is whatever reaches
// the model, and what it offers changes with it. The two are always shown
// apart, so it is clear which one a setting or a message belongs to.
const (
	fromHost     = "host"
	fromProvider = "provider"
	fromHere     = ""
)

var commandName = regexp.MustCompile(`^/[a-z][a-z0-9_-]*$`)

// answer is what a command has to show, and whose it is. A command that
// moved to a session that holds something tells what it holds after it.
type answer struct {
	source string
	text   string
	told   *protocol.HistoryResult
}

// isCommand reports whether line is a command rather than a task. A path
// such as /etc/hosts is not one.
func isCommand(line string) bool {
	name, _, _ := strings.Cut(line, " ")

	return commandName.MatchString(name)
}

// label is the mark put on everything that comes from the host or from the
// provider. It says the source in words as well as in color.
func (u *ui) label(source string) string {
	switch source {
	case fromHost:
		return u.theme.host + "[host]" + styleReset
	case fromProvider:
		return u.theme.provider + "[provider " + u.info.Provider + "]" + styleReset
	}

	return ""
}

// command runs one command line and shows its answer.
func (u *ui) command(line string) {
	u.work(line, func() (answer, error) {
		return u.execute(strings.Fields(line))
	})
}

// work shows label as what was asked, does it in the background, and shows
// the answer. During a run it is still asked: the host answers what does not
// disturb the run and says what has to wait.
func (u *ui) work(label string, do func() (answer, error)) {
	u.mu.Lock()

	if u.asking {
		u.notice = "a command is waiting for its answer"
		u.footer()
		u.mu.Unlock()

		return
	}

	u.asking = true
	u.notice = ""
	u.input.clear()
	u.line(styleBold, "\n"+echoed(label))
	u.mu.Unlock()

	u.wait.Add(1)

	go func() {
		defer u.wait.Done()

		reply, err := do()

		u.mu.Lock()
		defer u.mu.Unlock()

		u.asking = false

		mark := u.label(reply.source)

		switch {
		case err != nil && mark != "":
			u.line("", mark+" "+u.theme.failure+err.Error())
		case err != nil:
			u.line(u.theme.failure, err.Error())
		case mark != "":
			u.line("", mark+"\n"+styleDim+reply.text)
		default:
			u.line(styleDim, reply.text)
		}

		if reply.told != nil {
			u.retell(*reply.told)
		}

		u.footer()
	}()
}

func (u *ui) execute(words []string) (answer, error) {
	name, args := strings.TrimPrefix(words[0], "/"), words[1:]

	switch words[0] {
	case commandHelp:
		return answer{text: u.help()}, nil
	case commandCopy:
		return u.copyAnswer(args)
	case commandExport:
		return u.export(args)
	}

	// The host's functions come first: they need no provider to be running.
	if index := slices.IndexFunc(u.info.Functions, func(function protocol.Function) bool { return function.Name == name }); index >= 0 {
		return u.call(fromHost, u.info.Functions[index], args)
	}

	described, err := u.describe()

	if err != nil {
		return answer{source: fromProvider}, err
	}

	if index := slices.IndexFunc(described.Offers.Functions, func(function protocol.Function) bool { return function.Name == name }); index >= 0 {
		return u.call(fromProvider, described.Offers.Functions[index], args)
	}

	return answer{}, fmt.Errorf("there is no command %s; see %s", words[0], commandHelp)
}

// call runs a function of the host or of the provider. One that asks to be
// confirmed is not run yet: it waits for the user to say yes.
func (u *ui) call(source string, function protocol.Function, args []string) (answer, error) {
	params, err := arguments(function, args)

	if err != nil {
		return answer{source: source}, err
	}

	invoke := func() (answer, error) {
		if source == fromHost {
			return u.host(function, params)
		}

		return u.provider(function, params)
	}

	if function.Confirm == "" {
		return invoke()
	}

	u.mu.Lock()
	u.pending = invoke
	u.mu.Unlock()

	return answer{source: source, text: function.Confirm + "\nType yes to go ahead. Anything else leaves things as they are."}, nil
}

// confirm takes the line typed after a function asked to be confirmed, and
// reports whether there was one waiting.
func (u *ui) confirm(line string) bool {
	u.mu.Lock()
	pending := u.pending
	u.pending = nil
	u.mu.Unlock()

	if pending == nil {
		return false
	}

	if strings.EqualFold(line, "yes") {
		u.work(line, pending)
		return true
	}

	u.work(line, func() (answer, error) {
		return answer{text: "nothing was done"}, nil
	})

	return true
}

// describe asks what the provider offers, and keeps the answer for naming
// and completing its commands until the provider changes.
func (u *ui) describe() (protocol.DescribeResult, error) {
	described, err := u.conn.Describe()

	if err != nil {
		return described, err
	}

	u.mu.Lock()
	u.learned(described)
	u.mu.Unlock()

	return described, nil
}

// learned keeps what the provider offers, and rebuilds a menu of commands
// opened before it was known, so the provider's are in it. The caller holds
// the lock.
func (u *ui) learned(described protocol.DescribeResult) {
	u.described = &described

	if u.menu != nil && u.menu.head == "" {
		u.menu = nil
		u.refreshMenu()
		u.footer()
	}
}

// groupTitles say what each group of the host's functions acts on.
var groupTitles = map[string]string{
	protocol.GroupSystem:   "system: rumbo itself, the same for every session and provider",
	protocol.GroupSession:  "session: the current session, and the others of this project",
	protocol.GroupProvider: "providers: which there are, how each is started, and what is set for each",
}

// groupTitle names a group of the host's functions, also one this front end
// does not know.
func groupTitle(group string) string {
	if title, known := groupTitles[group]; known {
		return title
	}

	if group == "" {
		return "other"
	}

	return group
}

// help lists every command there is, grouped by what it acts on.
func (u *ui) help() string {
	var out strings.Builder

	fmt.Fprintf(&out, "%s%s  rumbo itself, the same with any provider. Its commands, by what they act on:\n", styleReset, u.label(fromHost))

	// The groups come in the order the host lists its functions.
	var groups []string
	members := map[string][]protocol.Function{}

	for _, function := range commands(u.info.Functions) {
		if _, seen := members[function.Group]; !seen {
			groups = append(groups, function.Group)
		}

		members[function.Group] = append(members[function.Group], function)
	}

	for _, group := range groups {
		fmt.Fprintf(&out, "\n%s%s\n", styleReset, groupTitle(group))

		for _, function := range members[group] {
			fmt.Fprintf(&out, "%s%s\n", styleDim, usage(function))
		}
	}

	// Only the provider can say what it offers, so it is asked.
	described, err := u.describe()

	fmt.Fprintf(&out, "\n%s%s  what reaches the model. What it offers changes with the provider; /provider-show shows its settings.\n%s", styleReset, u.label(fromProvider), styleDim)

	if err != nil {
		fmt.Fprintf(&out, "%sIts own commands could not be listed: %s%s\n", u.theme.failure, err, styleDim)
	} else {
		for _, function := range commands(described.Offers.Functions) {
			fmt.Fprintf(&out, "%s\n", usage(function))
		}
	}

	fmt.Fprintf(&out, "\n%sthe terminal UI's own\n%s", styleReset, styleDim)

	for _, function := range ownCommands {
		fmt.Fprintf(&out, "%s\n", usage(function))
	}

	out.WriteString("\n")
	out.WriteString("A slash opens a menu of the commands, and after one, of what it takes: Up and Down choose, Tab or Enter take, Escape closes it. Enter on a command that still needs something opens what it takes next.\n")
	out.WriteString("Enter sends; Alt-Enter, Shift-Enter, or Ctrl-J starts a new line. Up and Down go through the lines typed, and past them through what you entered before, which Ctrl-R searches.\n")
	out.WriteString("Anything that is not a command is a task.")

	return out.String()
}

// commands are the functions offered as commands: all but those there to
// list the values a parameter takes, which the menu offers in its place.
func commands(functions []protocol.Function) []protocol.Function {
	return slices.DeleteFunc(slices.Clone(functions), func(function protocol.Function) bool { return function.Lists })
}

func usage(function protocol.Function) string {
	written := "/" + function.Name

	for _, param := range function.Params {
		written += " " + signature(param)
	}

	return fmt.Sprintf("%-28s %s", written, function.Description)
}

// signature is how a parameter is written in a command's usage: in angle
// brackets, or in square ones when it can be left out.
func signature(param protocol.Option) string {
	if param.Optional {
		return "[" + param.Name + "]"
	}

	return "<" + param.Name + ">"
}

// arguments turns the words after a command into the function's parameters,
// in the order the function lists them. The last one takes what is left.
func arguments(function protocol.Function, args []string) (json.RawMessage, error) {
	required := 0

	for _, param := range function.Params {
		if !param.Optional {
			required++
		}
	}

	if len(args) < required {
		return nil, fmt.Errorf("usage: %s", strings.TrimSpace(usage(protocol.Function{Name: function.Name, Params: function.Params})))
	}

	params := map[string]json.RawMessage{}

	for index, param := range function.Params {
		if index >= len(args) {
			break
		}

		value := args[index]

		if index == len(function.Params)-1 {
			value = strings.Join(args[index:], " ")
		}

		encoded, err := encode(param, value)

		if err != nil {
			return nil, err
		}

		params[param.Name] = encoded
	}

	return json.Marshal(params)
}

// host calls a function of the host and takes in the state it answers with.
func (u *ui) host(function protocol.Function, params json.RawMessage) (answer, error) {
	result, err := u.conn.InvokeHost(function.Name, params)

	if err != nil {
		return answer{source: fromHost}, err
	}

	u.mu.Lock()
	moved := result.State.Session != u.session
	u.mu.Unlock()

	// A session opened or branched to shows what it holds.
	var told *protocol.HistoryResult

	if moved && result.State.Session != "" {
		if history, err := u.conn.History(result.State.Session); err == nil {
			told = &history
		}
	}

	u.mu.Lock()

	// What the status bar counts is the current session's, which its
	// history tells again.
	if moved {
		u.usage = render.Usage{}
	}

	// What is known of the provider was for the provider that was in use,
	// and the new one is asked at once, for its commands.
	if result.State.Provider != u.info.Provider {
		u.described = nil
		u.wait.Go(func() { _, _ = u.describe() })
	}

	u.info.Provider, u.info.Model, u.info.Dir = result.State.Provider, result.State.Model, result.State.Dir
	u.session = result.State.Session

	// The current session may have changed, and a run left behind goes on
	// in the background.
	u.follow()
	u.mu.Unlock()

	return answer{source: fromHost, text: strings.TrimSpace(result.Text + "\n" + list(result.Values)), told: told}, nil
}

// provider calls a function of the provider and shows what it answers.
func (u *ui) provider(function protocol.Function, params json.RawMessage) (answer, error) {
	named := func(id int64) {
		u.mu.Lock()
		defer u.mu.Unlock()

		u.invoking = id
	}

	raw, err := u.conn.InvokeWith(function.Name, params, named, talk{ui: u})

	u.mu.Lock()
	u.invoking = 0
	u.mu.Unlock()

	if err != nil {
		return answer{source: fromProvider}, err
	}

	var listed protocol.ValuesResult

	if json.Unmarshal(raw, &listed) == nil && len(listed.Values) > 0 {
		return answer{source: fromProvider, text: list(listed.Values)}, nil
	}

	// A function that answers in words is shown as words, not as JSON.
	var text string

	if json.Unmarshal(raw, &text) == nil {
		return answer{source: fromProvider, text: text}, nil
	}

	return answer{source: fromProvider, text: string(raw)}, nil
}

// talk is the Dialog of a provider's function: what it says is shown marked
// as the provider's, and what it asks is answered by the next line typed.
type talk struct {
	ui *ui
}

func (t talk) Say(text string) {
	u := t.ui

	u.mu.Lock()
	defer u.mu.Unlock()

	u.line("", u.label(fromProvider)+" "+text)
}

func (t talk) Ask(ctx context.Context, text string, secret bool) (string, error) {
	u := t.ui
	asked := &question{secret: secret, answer: make(chan string, 1)}

	u.mu.Lock()
	u.question = asked
	u.input.clear()
	u.notice = ""
	u.line("", u.label(fromProvider)+" "+text)
	u.notify("rumbo: the provider asks: " + text)
	u.mu.Unlock()

	select {
	case answer := <-asked.answer:
		return answer, nil

	case <-ctx.Done():
		u.mu.Lock()
		defer u.mu.Unlock()

		// What was typed of a secret answer goes with the question.
		if u.question == asked {
			u.question = nil

			if secret {
				u.input.clear()
			}

			u.footer()
		}

		return "", ctx.Err()
	}
}

// answer gives the line typed to the question waiting for it. A secret
// answer is not shown, and no answer is kept in the history. The caller
// holds the lock.
func (u *ui) answer(asked *question) {
	text := u.input.String()
	shown := text

	if asked.secret {
		shown = "(not shown)"
	}

	u.question = nil
	u.input.clear()
	u.line(styleBold, "\n"+echoed(shown))
	asked.answer <- text
}

// list shows values one per line, with what describes each.
func list(values []protocol.Value) string {
	lines := make([]string, len(values))

	for index, value := range values {
		lines[index] = value.Value

		for _, detail := range []string{value.Label, value.Description} {
			if detail != "" {
				lines[index] += "  " + detail
			}
		}
	}

	return strings.Join(lines, "\n")
}

// encode turns what the user typed into the JSON value the option's type
// asks for.
func encode(option protocol.Option, value string) (json.RawMessage, error) {
	switch option.Type {
	case protocol.OptionBoolean:
		parsed, err := strconv.ParseBool(value)

		if err != nil {
			return nil, fmt.Errorf("%s is true or false", option.Name)
		}

		return json.Marshal(parsed)

	case protocol.OptionNumber:
		parsed, err := strconv.ParseFloat(value, 64)

		if err != nil {
			return nil, fmt.Errorf("%s is a number", option.Name)
		}

		return json.Marshal(parsed)
	}

	if len(option.Enum) > 0 && !slices.Contains(option.Enum, value) {
		return nil, fmt.Errorf("%s is one of: %s", option.Name, strings.Join(option.Enum, ", "))
	}

	return json.Marshal(value)
}
