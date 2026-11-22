package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LiasCode/rumbo/protocol"
)

// The picker changes the model and the options of the session shown, which
// the session keeps, as rumbo-gui's does: the provider and the model are
// the parameters of the host's use, with the values it lists for them, and
// the options are those the provider describes, each changed with the
// host's session-set, as name=value, which checks it against what the
// provider describes. A run in progress goes on with what it started with.

// What a field of the picker changes.
const (
	roleUse = iota
	roleOption
)

// picking is what the picker opened with: what each field changes, and
// what it held.
type picking struct {
	roles   []int
	initial []string
}

// pickerForm is a form of the parameters of use, holding the provider and
// the model in use, and, when the host offers session-set, of the options
// the provider describes, each holding the value in force.
func pickerForm(use protocol.Function, sessionSet bool, described protocol.DescribeResult, host []protocol.Function) *form {
	shown := &form{
		title:       "model and options",
		description: "What this session uses from its next run on, and keeps.",
		host:        host,
		provider:    described.Offers.Functions,
		picking:     &picking{},
	}

	add := func(each *field, role int) {
		shown.fields = append(shown.fields, each)
		shown.picking.roles = append(shown.picking.roles, role)
		shown.picking.initial = append(shown.picking.initial, each.value.String())
	}

	for _, param := range use.Params {
		initial := map[string]string{"provider": described.Provider, "model": described.Model}[param.Name]
		empty := ""

		if param.Optional {
			empty = "leave it out"
		}

		add(newField(param, protocol.TargetHost, initial, empty), roleUse)
	}

	if !sessionSet {
		return shown
	}

	for index, option := range described.Offers.Options {
		each := newField(option, protocol.TargetProvider, inForce(option, described.Options), "as the provider has it")

		if index == 0 {
			each.heading = "Options of " + described.Provider
		}

		add(each, roleOption)
	}

	return shown
}

// inForce is the value an option has in the session, in words: what the
// provider says is in force, else its default.
func inForce(option protocol.Option, options json.RawMessage) string {
	var chosen map[string]json.RawMessage

	if json.Unmarshal(options, &chosen) == nil {
		if value, ok := chosen[option.Name]; ok {
			return readable(value)
		}
	}

	if len(option.Default) > 0 {
		return readable(option.Default)
	}

	return ""
}

// pickChange is a change the picker asks of the host, in the order it is
// sent: use with these parameters, or one option as name=value, where an
// empty value gives back what the session chose, so the provider's own
// applies.
type pickChange struct {
	use    json.RawMessage
	option string
}

// line is the command a change is, as it would be typed.
func (c pickChange) line() string {
	if c.option != "" {
		return "/session-set " + c.option
	}

	var params map[string]string
	_ = json.Unmarshal(c.use, &params)

	return strings.TrimSpace("/use " + params["provider"] + " " + params["model"])
}

// changes are what the picker's fields ask for. A new provider takes the
// options it offers instead of the old one's, so the options are left as
// they are then.
func (f *form) changes() ([]pickChange, error) {
	values := f.values()
	changed := func(index int) bool { return values[index] != strings.TrimSpace(f.picking.initial[index]) }

	var changes []pickChange

	using, newProvider := false, false

	for index, role := range f.picking.roles {
		if role == roleUse && changed(index) {
			using = true
			newProvider = newProvider || f.fields[index].param.Name == "provider"
		}
	}

	if using {
		params := map[string]json.RawMessage{}

		for index, role := range f.picking.roles {
			param := f.fields[index].param

			switch {
			case role != roleUse:
				continue
			case values[index] == "" && param.Optional:
				continue
			case values[index] == "":
				return nil, fmt.Errorf("%s is needed", param.Name)
			}

			encoded, err := encode(param, values[index])

			if err != nil {
				return nil, err
			}

			params[param.Name] = encoded
		}

		raw, _ := json.Marshal(params)
		changes = append(changes, pickChange{use: raw})
	}

	if newProvider {
		return changes, nil
	}

	for index, role := range f.picking.roles {
		if role != roleOption || !changed(index) {
			continue
		}

		param := f.fields[index].param

		// What the provider takes is the provider's to say, but a value of
		// the wrong type is caught here.
		if values[index] != "" {
			if _, err := encode(protocol.Option{Name: param.Name, Type: param.Type}, values[index]); err != nil {
				return nil, err
			}
		}

		changes = append(changes, pickChange{option: param.Name + "=" + values[index]})
	}

	return changes, nil
}
