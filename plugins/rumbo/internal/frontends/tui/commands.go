package tui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/LiasCode/rumbo/protocol"
)

// A line that starts with a slash and a word is a command, not a task: one
// of the terminal UI's own (verbs.go), or a function the host, the
// provider, or an extension described that none of those wraps, under its
// name.

// Where a command acts and where an answer comes from. The host is rumbo
// itself and is the same with any provider; the provider is whatever reaches
// the model, and what it offers changes with it; an extension is a program
// of the user's, named after fromExtensions. They are always shown apart,
// so it is clear which one a setting or a message belongs to.
const (
	fromHost       = "host"
	fromProvider   = "provider"
	fromExtensions = protocol.TargetExtension
)

// fromExtension is the source that names one extension.
func fromExtension(name string) string {
	return fromExtensions + " " + name
}

// extensionOf returns the extension a source names, if it names one.
func extensionOf(source string) (string, bool) {
	return strings.CutPrefix(source, fromExtensions+" ")
}

var commandName = regexp.MustCompile(`^/[a-z][a-z0-9_-]*$`)

// isCommand reports whether line is a command rather than a task. A path
// such as /etc/hosts is not one.
func isCommand(line string) bool {
	name, _, _ := strings.Cut(line, " ")

	return commandName.MatchString(name)
}

// commands are the functions offered as commands: all but those there to
// list the values a parameter takes, which the menu offers in its place.
func commands(functions []protocol.Function) []protocol.Function {
	return slices.DeleteFunc(slices.Clone(functions), func(function protocol.Function) bool { return function.Lists })
}

func usage(function protocol.Function) string {
	written := "/" + function.Name
	order := ordered(function)

	for _, param := range function.Params {
		if !slices.ContainsFunc(order, func(each protocol.Option) bool { return each.Name == param.Name }) {
			written += " [" + param.Name + "=…]"

			continue
		}

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

// ordered are the parameters the words typed after a command go to, in
// turn. One that can be left out and comes before one that cannot is not
// among them: it is given by name, as name=value, so that leaving it out
// does not give its word to another.
func ordered(function protocol.Function) []protocol.Option {
	var order []protocol.Option

	for index, param := range function.Params {
		needed := func(later protocol.Option) bool { return !later.Optional }

		if param.Optional && slices.ContainsFunc(function.Params[index+1:], needed) {
			continue
		}

		order = append(order, param)
	}

	return order
}

// typedParams reads the words typed after a command as the values of the
// function's parameters. A word name=value gives the parameter of that name
// that can be left out, and the other words go to the ordered parameters in
// turn, the last of which takes the rest of the words as they are. It also
// returns the ordered parameters still without a value, in turn.
func typedParams(function protocol.Function, args []string) (map[string]string, []protocol.Option) {
	values := map[string]string{}
	pending := ordered(function)

	for index, word := range args {
		if len(pending) == 0 {
			break
		}

		name, value, found := strings.Cut(word, "=")
		_, taken := values[name]
		optional := slices.ContainsFunc(function.Params, func(param protocol.Option) bool { return param.Name == name && param.Optional })

		if found && optional && !taken {
			values[name] = value
			pending = slices.DeleteFunc(pending, func(param protocol.Option) bool { return param.Name == name })

			continue
		}

		// The last one takes the rest.
		if len(pending) == 1 && pending[0].Name == function.Params[len(function.Params)-1].Name {
			values[pending[0].Name] = strings.Join(args[index:], " ")

			return values, nil
		}

		values[pending[0].Name] = word
		pending = pending[1:]
	}

	return values, pending
}

// arguments turns the words after a command into the function's
// parameters, as typedParams reads them, in the types the function declares.
func arguments(function protocol.Function, args []string) (json.RawMessage, error) {
	values, _ := typedParams(function, args)
	params := map[string]json.RawMessage{}

	for _, param := range function.Params {
		value, found := values[param.Name]

		if !found {
			if param.Optional {
				continue
			}

			return nil, fmt.Errorf("usage: %s", strings.TrimSpace(usage(protocol.Function{Name: function.Name, Params: function.Params})))
		}

		encoded, err := encode(param, value)

		if err != nil {
			return nil, err
		}

		params[param.Name] = encoded
	}

	return json.Marshal(params)
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
