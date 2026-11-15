package tui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/LiasCode/rumbo/protocol"
)

// maxListed bounds how many values of an option are shown.
const maxListed = 40

// offered lists what can be chosen, with the value each choice has in the
// current session.
func (u *ui) offered(described protocol.DescribeResult) string {
	var inForce map[string]json.RawMessage
	_ = json.Unmarshal(described.Options, &inForce)

	var out strings.Builder

	fmt.Fprintf(&out, "provider: %s\nmodel = %s", described.Provider, described.Model)

	if offer := described.Offers.Model; offer != nil {
		out.WriteString(u.accepted(*offer))
	}

	for _, option := range described.Offers.Options {
		value := "(not set)"

		for _, source := range []json.RawMessage{option.Default, inForce[option.Name]} {
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

	fmt.Fprintf(&out, "\nthe session's commands and the providers' change them; see %s", commandHelp)

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
