package cli

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/LiasCode/rumbo/protocol"
)

// This file owns how a person changes a setting, at any level: name=value,
// where an empty value gives the setting back to the level under it, and how
// a value is read by what describes it. The command line and the host's
// functions both go through it, so a change is written the same way for the
// system, a provider, a front end, and a session.

// change is one name=value a person wrote.
type change struct {
	name  string
	value string
}

// back says the change gives its setting back to the level under it.
func (c change) back() bool {
	return c.value == ""
}

// parseChanges reads name=value changes, each name once.
func parseChanges(written []string) ([]change, error) {
	if len(written) == 0 {
		return nil, fmt.Errorf("nothing to change: give name=value; %s", emptyGivesBack)
	}

	var changes []change

	for _, each := range written {
		name, value, found := strings.Cut(each, "=")

		if !found || name == "" {
			return nil, fmt.Errorf("%q is not name=value; %s", each, emptyGivesBack)
		}

		if slices.ContainsFunc(changes, func(known change) bool { return known.name == name }) {
			return nil, fmt.Errorf("%s is given twice", name)
		}

		changes = append(changes, change{name: name, value: value})
	}

	return changes, nil
}

// emptyGivesBack says how a setting goes back to its default.
const emptyGivesBack = "an empty value, as in name=, gives it back to its default"

// encodeValue reads what a person typed as the JSON value a setting
// described as option takes: true or false, a number, or text, one of those
// it lists when it lists them. A setting described without a type is read
// as fieldValue reads it.
func encodeValue(option protocol.Option, text string) (json.RawMessage, error) {
	switch option.Type {
	case protocol.OptionBoolean:
		parsed, err := strconv.ParseBool(text)

		if err != nil {
			return nil, fmt.Errorf("%s is true or false, not %q", option.Name, text)
		}

		return json.Marshal(parsed)

	case protocol.OptionNumber:
		var number float64

		// What JSON reads as a number is kept as written, so 32768 stays a
		// whole number.
		if err := json.Unmarshal([]byte(text), &number); err != nil {
			return nil, fmt.Errorf("%s is a number, not %q", option.Name, text)
		}

		return json.RawMessage(text), nil

	case protocol.OptionString:
		if len(option.Enum) > 0 && !slices.Contains(option.Enum, text) {
			return nil, fmt.Errorf("%s is one of %s, not %q", option.Name, strings.Join(option.Enum, ", "), text)
		}

		return json.Marshal(text)
	}

	return fieldValue(text), nil
}

// typeOf describes a value found in a configuration in force, for a setting
// whose program does not describe it: as a number, true or false, or text,
// as the value is.
func typeOf(name string, value json.RawMessage) protocol.Option {
	option := protocol.Option{Name: name}

	switch trimmed := strings.TrimSpace(string(value)); {
	case trimmed == "true" || trimmed == "false":
		option.Type = protocol.OptionBoolean
	case strings.HasPrefix(trimmed, `"`):
		option.Type = protocol.OptionString
	case trimmed != "" && strings.ContainsAny(trimmed[:1], "-0123456789"):
		option.Type = protocol.OptionNumber
	}

	return option
}

// shownValue is a JSON value as a person reads it: text without its quotes,
// unless it is empty, and anything else as JSON writes it.
func shownValue(raw json.RawMessage) string {
	var text string

	if json.Unmarshal(raw, &text) == nil && text != "" {
		return text
	}

	return string(raw)
}

// chooseOptions lays changes over the option values chosen in a session,
// checking each against the options its provider describes: one the
// provider does not describe is refused, and a value is read as its
// option's type asks. It returns what is chosen afterwards, and a line
// saying each change.
func chooseOptions(provider string, described []protocol.Option, chosen json.RawMessage, written []string) (json.RawMessage, []string, error) {
	changes, err := parseChanges(written)

	if err != nil {
		return nil, nil, err
	}

	fields := map[string]json.RawMessage{}

	if len(chosen) > 0 {
		if err := json.Unmarshal(chosen, &fields); err != nil {
			return nil, nil, fmt.Errorf("the options chosen in the session are not a JSON object: %w", err)
		}
	}

	var said []string

	for _, each := range changes {
		if each.back() {
			if _, found := fields[each.name]; !found {
				return nil, nil, fmt.Errorf("%s is not chosen in the session, so it has nothing to give back", each.name)
			}

			delete(fields, each.name)
			said = append(said, each.name+": given back to "+provider+"'s")

			continue
		}

		index := slices.IndexFunc(described, func(option protocol.Option) bool { return option.Name == each.name })

		if index < 0 {
			return nil, nil, fmt.Errorf("%s has no option %q; %s", provider, each.name, optionNames(provider, described))
		}

		value, err := encodeValue(described[index], each.value)

		if err != nil {
			return nil, nil, err
		}

		fields[each.name] = value
		said = append(said, each.name+" = "+shownValue(value))
	}

	if len(fields) == 0 {
		return nil, said, nil
	}

	options, err := json.Marshal(fields)

	return options, said, err
}

// optionNames says which options a provider describes.
func optionNames(provider string, described []protocol.Option) string {
	if len(described) == 0 {
		return provider + " describes no options"
	}

	names := make([]string, len(described))

	for index, option := range described {
		names[index] = option.Name
	}

	return "its options are " + strings.Join(names, ", ")
}
