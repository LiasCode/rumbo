package tui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
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

// maxListed bounds how many values of an option are shown.
const maxListed = 40

var commandName = regexp.MustCompile(`^/[a-z][a-z0-9_-]*$`)

// isCommand reports whether line is a command rather than a task. A path
// such as /etc/hosts is not one.
func isCommand(line string) bool {
	name, _, _ := strings.Cut(line, " ")

	return commandName.MatchString(name)
}

// command runs one command and shows its answer, unless a run is in
// progress: commands go to the provider, which is busy during a run.
func (u *ui) command(line string) {
	u.mu.Lock()

	if u.running {
		u.notice = "still working; commands wait for the run to end"
		u.footer()
		u.mu.Unlock()

		return
	}

	u.running = true
	u.notice = ""
	u.input = nil
	u.line(styleBold, "\n"+prompt+line)
	u.mu.Unlock()

	u.wait.Add(1)

	go func() {
		defer u.wait.Done()

		answer, err := u.execute(strings.Fields(line))

		u.mu.Lock()
		defer u.mu.Unlock()

		u.running = false

		if err != nil {
			u.line(styleRed, err.Error())
		} else {
			u.line(styleDim, answer)
		}

		u.footer()
	}()
}

func (u *ui) execute(words []string) (string, error) {
	name, args := strings.TrimPrefix(words[0], "/"), words[1:]

	if words[0] == commandHelp {
		return u.help(), nil
	}

	// The host's functions come first: they need no provider to be running.
	if words[0] != commandOptions && words[0] != commandSet {
		if index := slices.IndexFunc(u.info.Functions, func(function protocol.Function) bool { return function.Name == name }); index >= 0 {
			return u.host(u.info.Functions[index], args)
		}
	}

	described, err := u.conn.Describe()

	if err != nil {
		return "", err
	}

	switch words[0] {
	case commandOptions:
		return u.offered(described), nil

	case commandSet:
		if len(args) == 0 {
			return "", fmt.Errorf("usage: %s <name> [value]; without a value the choice is dropped. See %s", commandSet, commandOptions)
		}

		return u.set(described, args[0], strings.Join(args[1:], " "))
	}

	if index := slices.IndexFunc(described.Offers.Functions, func(function protocol.Function) bool { return function.Name == name }); index >= 0 {
		return u.provider(described.Offers.Functions[index], args)
	}

	return "", fmt.Errorf("there is no command %s; see %s", words[0], commandHelp)
}

// help lists the commands. The provider's functions are left to /options,
// which is where the provider gets started.
func (u *ui) help() string {
	var out strings.Builder

	for _, function := range u.info.Functions {
		fmt.Fprintf(&out, "%s\n", usage(function))
	}

	fmt.Fprintf(&out, "%-28s %s\n", commandOptions, "Show what the provider offers and what is chosen")
	fmt.Fprintf(&out, "%-28s %s\n", commandSet+" <name> [value]", "Choose a model or an option for the tasks that follow")
	fmt.Fprintf(&out, "%-28s %s", commandHelp, "Show this list")

	return out.String()
}

func usage(function protocol.Function) string {
	signature := "/" + function.Name

	for _, param := range function.Params {
		signature += " <" + param.Name + ">"
	}

	return fmt.Sprintf("%-28s %s", signature, function.Description)
}

// arguments turns the words after a command into the function's parameters,
// in the order the function lists them. The last one takes what is left.
func arguments(function protocol.Function, args []string) (json.RawMessage, error) {
	if len(args) < len(function.Params) {
		return nil, fmt.Errorf("usage: %s", strings.TrimSpace(usage(protocol.Function{Name: function.Name, Params: function.Params})))
	}

	params := map[string]json.RawMessage{}

	for index, param := range function.Params {
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
func (u *ui) host(function protocol.Function, args []string) (string, error) {
	params, err := arguments(function, args)

	if err != nil {
		return "", err
	}

	result, err := u.conn.InvokeHost(function.Name, params)

	if err != nil {
		return "", err
	}

	u.mu.Lock()

	// What was chosen here was chosen for the provider that was in use.
	if result.State.Provider != u.info.Provider {
		u.model, u.options = "", nil
	}

	u.info.Provider, u.info.Model, u.info.Dir = result.State.Provider, result.State.Model, result.State.Dir
	u.session = result.State.Session
	u.mu.Unlock()

	return strings.TrimSpace(result.Text + "\n" + list(result.Values)), nil
}

// provider calls a function of the provider and shows what it answers.
func (u *ui) provider(function protocol.Function, args []string) (string, error) {
	params, err := arguments(function, args)

	if err != nil {
		return "", err
	}

	raw, err := u.conn.Invoke(function.Name, params)

	if err != nil {
		return "", err
	}

	var listed protocol.ValuesResult

	if json.Unmarshal(raw, &listed) == nil && len(listed.Values) > 0 {
		return list(listed.Values), nil
	}

	return string(raw), nil
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

// offered lists what can be chosen, with the value each choice has now.
func (u *ui) offered(described protocol.DescribeResult) string {
	u.mu.Lock()
	model, chosen := u.model, u.options
	u.mu.Unlock()

	if model == "" {
		model = described.Model
	}

	var stored map[string]json.RawMessage
	_ = json.Unmarshal(described.Options, &stored)

	var out strings.Builder

	fmt.Fprintf(&out, "provider: %s\nmodel = %s", described.Provider, model)

	if offer := described.Offers.Model; offer != nil {
		out.WriteString(u.accepted(*offer))
	}

	for _, option := range described.Offers.Options {
		value := "(not set)"

		for _, source := range []json.RawMessage{option.Default, stored[option.Name], chosen[option.Name]} {
			if len(source) > 0 {
				value = strings.Trim(string(source), `"`)
			}
		}

		fmt.Fprintf(&out, "\n%s = %s", option.Name, value)

		if option.Description != "" {
			fmt.Fprintf(&out, "\n    %s", option.Description)
		}

		out.WriteString(u.accepted(option))
	}

	if len(described.Offers.Options) == 0 {
		out.WriteString("\n(this provider describes no options)")
	}

	fmt.Fprintf(&out, "\nchange one with: %s <name> <value>", commandSet)

	return out.String()
}

// accepted describes the values an option takes, asking the provider for
// them when the option names a function.
func (u *ui) accepted(option protocol.Option) string {
	values := option.Enum

	if option.Values != "" {
		raw, err := u.conn.Invoke(option.Values, nil)

		if err != nil {
			return "\n    values: " + err.Error()
		}

		var listed protocol.ValuesResult
		_ = json.Unmarshal(raw, &listed)

		for _, value := range listed.Values {
			values = append(values, value.Value)
		}
	}

	if len(values) == 0 {
		return ""
	}

	more := ""

	if len(values) > maxListed {
		more = fmt.Sprintf(", and %d more", len(values)-maxListed)
		values = values[:maxListed]
	}

	return "\n    values: " + strings.Join(values, ", ") + more
}

// set records a choice for the runs started from this front end.
func (u *ui) set(described protocol.DescribeResult, name, value string) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if name == "model" {
		u.model = value

		if value == "" {
			return "model is back to " + described.Model, nil
		}

		return "model = " + value, nil
	}

	index := slices.IndexFunc(described.Offers.Options, func(option protocol.Option) bool { return option.Name == name })

	if index < 0 {
		return "", fmt.Errorf("the provider describes no option %q; see %s", name, commandOptions)
	}

	if value == "" {
		delete(u.options, name)

		return name + " is back to its stored value", nil
	}

	encoded, err := encode(described.Offers.Options[index], value)

	if err != nil {
		return "", err
	}

	if u.options == nil {
		u.options = map[string]json.RawMessage{}
	}

	u.options[name] = encoded

	return name + " = " + value, nil
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

// choices are the run parameters for what the user has chosen so far.
func (u *ui) choices(params protocol.RunParams) protocol.RunParams {
	u.mu.Lock()
	defer u.mu.Unlock()

	params.Model = u.model

	if len(u.options) > 0 {
		params.Options, _ = json.Marshal(u.options)
	}

	return params
}
