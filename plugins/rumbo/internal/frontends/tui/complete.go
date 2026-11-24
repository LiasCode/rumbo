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
// takes, so /use offers the providers and the models of the one named
// before, without this file knowing either.

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
// front end's own, the host's functions, and the provider's when described
// is known, by name.
func commandChoicesOf(host []protocol.Function, described *protocol.DescribeResult) []choice {
	var choices []choice

	for _, function := range ownCommands {
		choices = append(choices, choice{value: "/" + function.Name, description: function.Description})
	}

	for _, function := range commands(host) {
		if !slices.ContainsFunc(choices, func(known choice) bool { return known.value == "/"+function.Name }) {
			choices = append(choices, choice{value: "/" + function.Name, description: function.Description})
		}
	}

	if described != nil {
		for _, function := range commands(described.Offers.Functions) {
			name := "/" + function.Name

			if !slices.ContainsFunc(choices, func(known choice) bool { return known.value == name }) {
				choices = append(choices, choice{value: name, description: function.Description})
			}
		}
	}

	for _, each := range extensionCommands(described) {
		name := "/" + each.function.Name

		if !slices.ContainsFunc(choices, func(known choice) bool { return known.value == name }) {
			choices = append(choices, choice{value: name, description: each.function.Description})
		}
	}

	slices.SortFunc(choices, func(a, b choice) int { return strings.Compare(a.value, b.value) })

	return choices
}

// lacksParams reports whether a command line gives fewer words than its
// function, among the front end's own, the host's, and the provider's
// described, has parameters it needs.
func lacksParams(host []protocol.Function, described *protocol.DescribeResult, line string) bool {
	words := strings.Fields(line)

	if len(words) == 0 {
		return false
	}

	name := strings.TrimPrefix(words[0], "/")
	functions := append(slices.Clone(ownCommands), host...)

	if described != nil {
		functions = append(slices.Clone(functions), described.Offers.Functions...)
	}

	for _, each := range extensionCommands(described) {
		functions = append(functions, each.function)
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
