package tui

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/LiasCode/rumbo/protocol"
)

// Completion works from the same descriptions the commands do. A command's
// name is completed from the functions the host and the provider described,
// and an argument from what its parameter says it accepts: fixed values, or
// the function that lists them, given the arguments before it that it
// takes, so /open offers sessions with their titles and /use the models of
// the provider named before, without this file knowing either.

// refreshMenu works out the menu for what is typed: commands while the
// first word is, and then the values of the parameter the word being typed
// is for. Values a function has to list are asked for in the background,
// and the menu shows them when they come. The caller holds the lock.
func (u *ui) refreshMenu() {
	line := u.input.String()

	if u.dismissed || u.question != nil || u.searching || !isCommandStart(line) {
		u.menu = nil
		return
	}

	head, token := splitLast(line)

	// The selection stays where Up and Down took it until the word changes.
	if u.menu != nil && u.menu.head == head {
		if !u.menu.loading && u.menu.token != token {
			u.menu.filter(token)
		}

		return
	}

	asked := &menu{head: head}
	u.menu = asked

	if head == "" {
		asked.choices = u.commandChoices()
		asked.filter(token)

		return
	}

	asked.loading = true

	u.wait.Go(func() {
		choices, hint := u.offer(head)

		u.mu.Lock()
		defer u.mu.Unlock()

		// The input may have moved on to another menu meanwhile.
		if u.menu != asked {
			return
		}

		_, token := splitLast(u.input.String())

		asked.loading, asked.choices, asked.hint = false, choices, hint
		asked.filter(token)
		u.footer()
	})
}

// isCommandStart reports whether line is a command being typed: a slash, a
// word, and maybe its arguments, all on one line.
func isCommandStart(line string) bool {
	name, _, _ := strings.Cut(line, " ")

	return !strings.Contains(line, "\n") && (name == "/" || commandName.MatchString(name))
}

// offer returns what the word after head can be, with what describes each,
// or a hint saying what it is or why there is nothing to offer. It may ask
// the host or the provider, so it runs without the lock.
func (u *ui) offer(head string) (choices []choice, hint string) {
	words := strings.Fields(head)
	position := len(words) - 1

	function, target, found := u.function(words[0])

	if !found {
		return nil, ""
	}

	if position >= len(function.Params) {
		return nil, ""
	}

	given := map[string]string{}

	for index, param := range function.Params[:position] {
		given[param.Name] = words[index+1]
	}

	param := function.Params[position]
	choices, hint = u.values(param, target, given)

	// What takes any value says what it is.
	if len(choices) == 0 && hint == "" {
		hint = strings.TrimSpace(signature(param) + "  " + param.Description)
	}

	return choices, hint
}

// function finds what a command names: a function of the host, or one of
// the provider, which has to be asked what it offers.
func (u *ui) function(command string) (protocol.Function, string, bool) {
	name := strings.TrimPrefix(command, "/")

	if index := slices.IndexFunc(ownCommands, func(function protocol.Function) bool { return function.Name == name }); index >= 0 {
		return ownCommands[index], fromHere, true
	}

	u.mu.Lock()
	index := slices.IndexFunc(u.info.Functions, func(function protocol.Function) bool { return function.Name == name })
	u.mu.Unlock()

	// The host's come next, as they do when a command runs.
	if index >= 0 {
		return u.info.Functions[index], protocol.TargetHost, true
	}

	// What the provider offers is asked once, and again when it changes.
	u.mu.Lock()
	described := u.described
	u.mu.Unlock()

	if described == nil {
		asked, err := u.describe()

		if err != nil {
			return protocol.Function{}, "", false
		}

		described = &asked
	}

	if index := slices.IndexFunc(described.Offers.Functions, func(function protocol.Function) bool { return function.Name == name }); index >= 0 {
		return described.Offers.Functions[index], protocol.TargetProvider, true
	}

	return protocol.Function{}, "", false
}

// values returns what an option takes, asking its function when it names
// one, with what was given for the parameters before it that the function
// takes; target says whose function that is. When the function cannot
// answer, the hint says why.
func (u *ui) values(option protocol.Option, target string, given map[string]string) ([]choice, string) {
	if option.Type == protocol.OptionBoolean {
		return []choice{{value: "true"}, {value: "false"}}, ""
	}

	if option.Values == "" {
		choices := make([]choice, len(option.Enum))

		for index, value := range option.Enum {
			choices[index] = choice{value: value}
		}

		return choices, ""
	}

	var listed []protocol.Value

	params := u.passed(option.Values, target, given)

	if target == protocol.TargetHost {
		result, err := u.conn.InvokeHost(option.Values, params)

		if err != nil {
			return nil, err.Error()
		}

		listed = result.Values
	} else {
		raw, err := u.conn.Invoke(option.Values, params)

		if err != nil {
			return nil, err.Error()
		}

		var result protocol.ValuesResult
		_ = json.Unmarshal(raw, &result)
		listed = result.Values
	}

	choices := make([]choice, len(listed))

	for index, value := range listed {
		choices[index] = choice{value: value.Value, label: value.Label, description: value.Description}
	}

	return choices, ""
}

// passed is what a function that lists values is given: the arguments,
// among those given, for the parameters it takes.
func (u *ui) passed(name, target string, given map[string]string) json.RawMessage {
	u.mu.Lock()
	functions := u.info.Functions

	if target == protocol.TargetProvider && u.described != nil {
		functions = u.described.Offers.Functions
	}

	u.mu.Unlock()

	index := slices.IndexFunc(functions, func(function protocol.Function) bool { return function.Name == name })

	if index < 0 {
		return nil
	}

	params := map[string]string{}

	for _, param := range functions[index].Params {
		if value := given[param.Name]; value != "" {
			params[param.Name] = value
		}
	}

	if len(params) == 0 {
		return nil
	}

	raw, _ := json.Marshal(params)

	return raw
}

// commandChoices returns every command known without asking anyone, with
// what each does: the front end's own, the host's, and the provider's once
// it has been described. The caller holds the lock.
func (u *ui) commandChoices() []choice {
	var choices []choice

	for _, function := range ownCommands {
		choices = append(choices, choice{value: "/" + function.Name, description: function.Description})
	}

	for _, function := range commands(u.info.Functions) {
		if !slices.ContainsFunc(choices, func(known choice) bool { return known.value == "/"+function.Name }) {
			choices = append(choices, choice{value: "/" + function.Name, description: function.Description})
		}
	}

	if u.described != nil {
		for _, function := range commands(u.described.Offers.Functions) {
			name := "/" + function.Name

			if !slices.ContainsFunc(choices, func(known choice) bool { return known.value == name }) {
				choices = append(choices, choice{value: name, description: function.Description})
			}
		}
	}

	slices.SortFunc(choices, func(a, b choice) int { return strings.Compare(a.value, b.value) })

	return choices
}

// needsMore reports whether a command line still lacks a parameter its
// function needs, so taking a choice from the menu does not send it yet.
// It only looks at what is known already. The caller holds the lock.
func (u *ui) needsMore(line string) bool {
	words := strings.Fields(line)

	if len(words) == 0 {
		return false
	}

	name := strings.TrimPrefix(words[0], "/")
	functions := append(slices.Clone(ownCommands), u.info.Functions...)

	if u.described != nil {
		functions = append(slices.Clone(functions), u.described.Offers.Functions...)
	}

	index := slices.IndexFunc(functions, func(function protocol.Function) bool { return function.Name == name })

	if index < 0 {
		return false
	}

	required := 0

	for _, param := range functions[index].Params {
		if !param.Optional {
			required++
		}
	}

	return len(words)-1 < required
}

// splitLast separates the word being typed from what comes before it.
func splitLast(line string) (head, token string) {
	index := strings.LastIndexByte(line, ' ')

	return line[:index+1], line[index+1:]
}
