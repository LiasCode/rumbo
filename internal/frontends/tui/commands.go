package tui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"rumbo/protocol"
)

// Commands the input line understands instead of sending it as a task. They
// show and change what the provider lets the user choose, and work from the
// provider's own description: nothing here knows any provider.
const (
	commandOptions = "/options"
	commandSet     = "/set"
)

// maxListed bounds how many values of an option are shown.
const maxListed = 40

// isCommand reports whether line is a command rather than a task.
func isCommand(line string) bool {
	name, _, _ := strings.Cut(line, " ")

	return name == commandOptions || name == commandSet
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
	described, err := u.conn.Describe()

	if err != nil {
		return "", err
	}

	if words[0] == commandOptions {
		return u.offered(described), nil
	}

	if len(words) < 2 {
		return "", fmt.Errorf("usage: %s <name> [value]; without a value the choice is dropped. See %s", commandSet, commandOptions)
	}

	return u.set(described, words[1], strings.Join(words[2:], " "))
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
