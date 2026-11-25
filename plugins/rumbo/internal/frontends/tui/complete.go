package tui

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/LiasCode/rumbo/protocol"
)

// Completion works from the same descriptions the commands do. A command's
// name is completed from the terminal UI's commands, which complete their
// own arguments (verbs.go), and from the host's functions no command wraps,
// whose arguments are completed from what each parameter says it accepts:
// fixed values, or the function that lists them, given the arguments
// before it that it takes.

// isCommandStart reports whether line is a command being typed: a slash, a
// word, and maybe its arguments, all on one line.
func isCommandStart(line string) bool {
	name, _, _ := strings.Cut(line, " ")

	return !strings.Contains(line, "\n") && (name == "/" || commandName.MatchString(name))
}

// listValues returns what an option takes: true and false, its fixed
// values, or what the function it names lists when given params, asked of
// the host or the provider as target says. When the function cannot
// answer, the hint says why.
func listValues(conn *protocol.FrontendConn, option protocol.Option, target string, params json.RawMessage) ([]choice, string) {
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

	listed, _, err := listFrom(conn, target, option.Values, params)

	if err != nil {
		return nil, err.Error()
	}

	choices := make([]choice, len(listed))

	for index, value := range listed {
		choices[index] = choice{value: value.Value, label: value.Label, description: value.Description}
	}

	return choices, ""
}

// listFrom calls the function of target that lists values, the host's, the
// provider's, or an extension's, and returns them with what the host said.
func listFrom(conn *protocol.FrontendConn, target, function string, params json.RawMessage) ([]protocol.Value, string, error) {
	if target == protocol.TargetHost {
		result, err := conn.InvokeHost(function, params)

		return result.Values, result.Text, err
	}

	var raw json.RawMessage
	var err error

	if extension, named := extensionOf(target); named {
		raw, err = conn.InvokeExtension(extension, function, params, nil, nil)
	} else {
		raw, err = conn.Invoke(function, params)
	}

	var result protocol.ValuesResult

	if err == nil {
		err = json.Unmarshal(raw, &result)
	}

	return result.Values, "", err
}

// passedTo is what the function name, among functions, is given to list
// values: the arguments, among those given, for the parameters it takes.
func passedTo(functions []protocol.Function, name string, given map[string]string) json.RawMessage {
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

// commandChoicesOf returns the commands there are with what each does: the
// terminal UI's own first, then the functions of the host no command
// wraps.
func commandChoicesOf(host []protocol.Function) []choice {
	var choices []choice

	for _, each := range verbs() {
		choices = append(choices, choice{value: "/" + each.name, label: each.args, description: each.description, example: each.example()})
	}

	var more []choice

	add := func(function protocol.Function) {
		name := "/" + function.Name

		if !slices.ContainsFunc(choices, func(known choice) bool { return known.value == name }) && !slices.ContainsFunc(more, func(known choice) bool { return known.value == name }) {
			more = append(more, choice{value: name, description: function.Description})
		}
	}

	for _, function := range commands(host) {
		if !isWrapped(function.Name) {
			add(function)
		}
	}

	slices.SortFunc(more, func(a, b choice) int { return strings.Compare(a.value, b.value) })

	return append(choices, more...)
}

// lacksParams reports whether a command line gives fewer words than its
// command needs: one of the terminal UI's own, or a function of the host.
func lacksParams(host []protocol.Function, line string) bool {
	words := strings.Fields(line)

	if len(words) == 0 {
		return false
	}

	if each, found := verbNamed(words[0]); found {
		return each.needs() && len(words) == 1
	}

	function, _, found := lookup(words[0], host)

	if !found {
		return false
	}

	required := 0

	for _, param := range function.Params {
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

// closest is the command meant by a name that is none: the one it starts,
// when only one does, or else the one a typo or two away from it.
func closest(name string, commands []choice) string {
	var started []string

	for _, each := range commands {
		if strings.HasPrefix(each.value, name) {
			started = append(started, each.value)
		}
	}

	if len(started) == 1 {
		return started[0]
	}

	best, nearest := "", 3

	for _, each := range commands {
		if apart := distance(name, each.value); apart < nearest {
			best, nearest = each.value, apart
		}
	}

	return best
}

// distance is how many runes must be added, taken away, or changed to make
// one word the other.
func distance(one, other string) int {
	a, b := []rune(one), []rune(other)
	previous := make([]int, len(b)+1)

	for index := range previous {
		previous[index] = index
	}

	for i := 1; i <= len(a); i++ {
		current := make([]int, len(b)+1)
		current[0] = i

		for j := 1; j <= len(b); j++ {
			changed := 1

			if a[i-1] == b[j-1] {
				changed = 0
			}

			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+changed)
		}

		previous = current
	}

	return previous[len(b)]
}
