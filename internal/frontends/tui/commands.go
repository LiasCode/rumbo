package tui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"rumbo/protocol"
)

// A line that starts with a slash and a word is a command, not a task. Three
// commands are the front end's own. Every other one is a function the host
// or the provider described: the front end knows none of them beforehand,
// and gains a command whenever either of them gains a function.
const (
	commandOptions = "/options"
	commandSet     = "/set"
	commandHelp    = "/help"
)

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

// answer is what a command has to show, and whose it is.
type answer struct {
	source string
	text   string
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
		return styleCyan + "[host]" + styleReset
	case fromProvider:
		return styleMagenta + "[provider " + u.info.Provider + "]" + styleReset
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
	u.input = nil
	u.line(styleBold, "\n"+prompt+label)
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
			u.line("", mark+" "+styleRed+err.Error())
		case err != nil:
			u.line(styleRed, err.Error())
		case mark != "":
			u.line("", mark+"\n"+styleDim+reply.text)
		default:
			u.line(styleDim, reply.text)
		}

		u.footer()
	}()
}

func (u *ui) execute(words []string) (answer, error) {
	name, args := strings.TrimPrefix(words[0], "/"), words[1:]

	if words[0] == commandHelp {
		return answer{text: u.help()}, nil
	}

	// The host's functions come first: they need no provider to be running.
	if words[0] != commandOptions && words[0] != commandSet {
		if index := slices.IndexFunc(u.info.Functions, func(function protocol.Function) bool { return function.Name == name }); index >= 0 {
			return u.call(fromHost, u.info.Functions[index], args)
		}
	}

	described, err := u.describe()

	if err != nil {
		return answer{source: fromProvider}, err
	}

	switch words[0] {
	case commandOptions:
		return answer{source: fromProvider, text: u.offered(described)}, nil

	case commandSet:
		if len(args) == 0 {
			return answer{}, fmt.Errorf("usage: %s <name> [value]; without a value the choice is dropped. See %s", commandSet, commandOptions)
		}

		text, err := u.set(described, args[0], strings.Join(args[1:], " "))

		return answer{source: fromProvider, text: text}, err
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
	u.described = &described
	u.mu.Unlock()

	return described, nil
}

// help lists every command there is, grouped by what it acts on.
func (u *ui) help() string {
	var out strings.Builder

	fmt.Fprintf(&out, "%s%s  rumbo itself: sessions, providers, the conversation. The same with any provider.\n", styleReset, u.label(fromHost))

	for _, function := range u.info.Functions {
		fmt.Fprintf(&out, "%s%s\n", styleDim, usage(function))
	}

	// Only the provider can say what it offers, so it is asked.
	described, err := u.describe()

	fmt.Fprintf(&out, "\n%s%s  what reaches the model. What it offers changes with the provider.\n%s", styleReset, u.label(fromProvider), styleDim)
	fmt.Fprintf(&out, "%-28s %s\n", commandOptions, "Show what it offers and what is chosen")
	fmt.Fprintf(&out, "%-28s %s\n", commandSet+" <name> [value]", "Choose its model or one of its options")

	if err != nil {
		fmt.Fprintf(&out, "%sIts own commands could not be listed: %s%s\n", styleRed, err, styleDim)
	} else {
		for _, function := range described.Offers.Functions {
			fmt.Fprintf(&out, "%s\n", usage(function))
		}
	}

	fmt.Fprintf(&out, "\n%-28s %s\n", commandHelp, "Show this list")
	out.WriteString("Tab completes a command and its arguments. Anything else you type is a task.")

	return out.String()
}

func usage(function protocol.Function) string {
	signature := "/" + function.Name

	for _, param := range function.Params {
		if param.Optional {
			signature += " [" + param.Name + "]"
		} else {
			signature += " <" + param.Name + ">"
		}
	}

	return fmt.Sprintf("%-28s %s", signature, function.Description)
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

	// What was chosen here, and what is known of the provider, was for the
	// provider that was in use.
	if result.State.Provider != u.info.Provider {
		u.model, u.options, u.described = "", nil, nil
	}

	u.info.Provider, u.info.Model, u.info.Dir = result.State.Provider, result.State.Model, result.State.Dir
	u.session = result.State.Session
	u.mu.Unlock()

	return answer{source: fromHost, text: strings.TrimSpace(result.Text + "\n" + list(result.Values))}, nil
}

// provider calls a function of the provider and shows what it answers.
func (u *ui) provider(function protocol.Function, params json.RawMessage) (answer, error) {
	raw, err := u.conn.Invoke(function.Name, params)

	if err != nil {
		return answer{source: fromProvider}, err
	}

	var listed protocol.ValuesResult

	if json.Unmarshal(raw, &listed) == nil && len(listed.Values) > 0 {
		return answer{source: fromProvider, text: list(listed.Values)}, nil
	}

	return answer{source: fromProvider, text: string(raw)}, nil
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
