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

// isCommand reports whether line is a command rather than a task. A path
// such as /etc/hosts is not one.
func isCommand(line string) bool {
	name, _, _ := strings.Cut(line, " ")

	return commandName.MatchString(name)
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
