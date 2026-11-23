package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/LiasCode/rumbo/internal/provider"
	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/protocol"
)

// What a setting can be set to is listed here, so a front end can offer it
// as a choice: the models of a provider, which only it knows; and, as the
// name=value changes system-set, session-set, and provider-set take, the
// values of each setting at that level, with the one in force marked. A
// mark goes in a value's description: its label is the name a person knows
// it by, which a front end may show in place of the value.

// providerModels lists the models a provider offers: the one named, or the
// current session's, with the one this session uses, and the one the
// provider is set to start with, marked.
func (h *host) providerModels(ctx context.Context, params map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	chosen := h.current.choice
	h.mu.Unlock()

	name := params["provider"]

	if name == "" {
		name = chosen.provider
	}

	described, pool, err := h.offers(ctx, name)

	if err != nil {
		return protocol.HostResult{}, err
	}

	if described.Model == nil {
		return protocol.HostResult{Text: name + " says nothing of how its models are named; give the one you want as it names it"}, nil
	}

	program, err := findProgram(h.database, store.KindProvider, name)

	if err != nil {
		return protocol.HostResult{}, err
	}

	values, err := choicesOf(ctx, pool, *described.Model)

	if err != nil {
		return protocol.HostResult{}, fmt.Errorf("the models of %s: %w", name, err)
	}

	for index, value := range values {
		if value.Value == program.Model {
			values[index].Description = remark(values[index].Description, "the one "+name+" starts with")
		}

		if name == chosen.provider && value.Value == chosen.model {
			values[index].Description = remark(values[index].Description, "in use in this session")
		}
	}

	return protocol.HostResult{Text: "the models " + name + " offers:", Values: values}, nil
}

// listModels starts a provider and lists the models it offers, marking the
// one it is set to start with.
func listModels(database store.Store, name string, diagnostics io.Writer) (string, error) {
	program, err := findProgram(database, store.KindProvider, name)

	if err != nil {
		return "", err
	}

	command, err := programCommand(program.Command)

	if err != nil {
		return "", err
	}

	process, err := provider.Start(context.Background(), command, program.Config, diagnostics)

	if err != nil {
		return "", err
	}

	defer process.Close()

	described := process.Description()

	if described.Model == nil {
		return name + " says nothing of how its models are named; give the one you want as it names it", nil
	}

	values, err := choicesOf(context.Background(), process, *described.Model)

	if err != nil {
		return "", fmt.Errorf("the models of %s: %w", name, err)
	}

	if len(values) == 0 {
		return name + " lists no models; give the one you want as it names it", nil
	}

	lines := make([]string, len(values))

	for index, value := range values {
		if value.Value == program.Model {
			value.Description = remark(value.Description, "the one "+name+" starts with")
		}

		parts := slices.DeleteFunc([]string{value.Value, value.Label, value.Description}, func(part string) bool { return part == "" })
		lines[index] = strings.Join(parts, "  ")
	}

	return strings.Join(lines, "\n"), nil
}

// systemValues lists the changes system-set takes, as name=value: the
// providers and front ends there are for those that name one, on and off for
// a switch, and the name alone for the others, which take what the person
// types. The values in
// force are marked, and a setting that is set comes with its empty value
// first, which gives it back to its default.
func (h *host) systemValues(context.Context, map[string]string) (protocol.HostResult, error) {
	stored, err := h.database.Settings()

	if err != nil {
		return protocol.HostResult{}, err
	}

	var values []protocol.Value

	for _, spec := range settingSpecs {
		current, set := stored[spec.key]
		back := "empty: gives it back to " + spec.fallbackText()

		var named []string

		switch spec.key {
		case keyDefaultProvider:
			named, err = programNames(h.database, store.KindProvider)
		case keyFrontend:
			named, err = programNames(h.database, store.KindFrontend)
		case keyComputer:
			named = []string{"on", "off"}
		}

		if err != nil {
			return protocol.HostResult{}, err
		}

		// One that takes what is typed is offered once, as its name.
		if named == nil {
			value := protocol.Value{Value: spec.key + "=", Description: spec.help}

			if set {
				value.Description = remark(value.Description, "in force: "+current+"; "+back)
			}

			values = append(values, value)

			continue
		}

		if set {
			values = append(values, protocol.Value{Value: spec.key + "=", Description: back})
		}

		for _, name := range named {
			value := protocol.Value{Value: spec.key + "=" + name, Description: spec.help}

			if set && current == name {
				value.Description = remark(value.Description, "in force")
			}

			values = append(values, value)
		}
	}

	return protocol.HostResult{Values: values}, nil
}

// programNames returns the names of the programs of a kind there are.
func programNames(database store.Store, kind string) ([]string, error) {
	programs, err := listPrograms(database, kind)

	if err != nil {
		return nil, err
	}

	names := make([]string, len(programs))

	for index, program := range programs {
		names[index] = program.Name
	}

	return names, nil
}

// sessionValues lists the changes session-set takes, as name=value: the
// values of each option of the current session's provider, those in force
// marked, after the empty value of each option the session chose, which
// gives it back to the provider's.
func (h *host) sessionValues(ctx context.Context, _ map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	chosen := h.current.choice
	h.mu.Unlock()

	described, pool, err := h.offers(ctx, chosen.provider)

	if err != nil {
		return protocol.HostResult{}, err
	}

	inForce, err := h.inForce(chosen, described)

	if err != nil {
		return protocol.HostResult{}, err
	}

	values, err := settingValues(ctx, pool, described.Options, objectFields(inForce), objectFields(chosen.options))

	if err != nil {
		return protocol.HostResult{}, err
	}

	if len(values) == 0 {
		return protocol.HostResult{Text: chosen.provider + " takes no options"}, nil
	}

	return protocol.HostResult{Values: values}, nil
}

// providerValues lists the changes provider-set takes for a provider, as
// name=value: the models it offers, and the values of the fields of its
// configuration and of its options, those set for it marked, after the
// empty value of each that is set, which gives it back to its default.
func (h *host) providerValues(ctx context.Context, params map[string]string) (protocol.HostResult, error) {
	name := params["provider"]

	h.mu.Lock()

	if name == "" {
		name = h.current.provider
	}

	h.mu.Unlock()

	chosen, _, err := chosenProgram(h.database, store.KindProvider, name)

	if err != nil {
		return protocol.HostResult{}, err
	}

	described, pool, err := h.offers(ctx, name)

	if err != nil {
		return protocol.HostResult{}, err
	}

	var values []protocol.Value

	if chosen.Model != "" {
		values = append(values, protocol.Value{Value: settingModel + "=", Description: "empty: takes it out"})
	}

	// The models need the provider to reach its server, which may not
	// answer; the other settings are listed all the same.
	if described.Model != nil {
		if models, err := choicesOf(ctx, pool, *described.Model); err == nil {
			for _, model := range models {
				value := protocol.Value{Value: settingModel + "=" + model.Value, Label: model.Label, Description: model.Description}

				if model.Value == chosen.Model {
					value.Description = remark(value.Description, "in force")
				}

				values = append(values, value)
			}
		}
	}

	set := objectFields(chosen.Config)
	settings := append(append([]protocol.Option{}, described.Fields...), described.Options...)
	listed, err := settingValues(ctx, pool, settings, set, set)

	if err != nil {
		return protocol.HostResult{}, err
	}

	return protocol.HostResult{Values: append(values, listed...)}, nil
}

// settingValues lists the name=value changes that settings take: the empty
// value of each one chosen, which gives it back, and then the values each
// takes, those in force marked. One that takes any value comes as its name,
// for the person to type the value.
func settingValues(ctx context.Context, pool *provider.Pool, settings []protocol.Option, inForce, chosen map[string]json.RawMessage) ([]protocol.Value, error) {
	var values []protocol.Value

	for _, setting := range settings {
		_, isChosen := chosen[setting.Name]

		choices, err := choicesOf(ctx, pool, setting)

		if err != nil {
			return nil, fmt.Errorf("the values of %s: %w", setting.Name, err)
		}

		// One that takes what is typed is offered once, as its name.
		if len(choices) == 0 {
			value := protocol.Value{Value: setting.Name + "=", Description: setting.Description}

			if isChosen {
				value.Description = remark(value.Description, "empty gives it back")
			}

			if raw, found := inForce[setting.Name]; found {
				value.Description = remark(value.Description, "in force: "+shownValue(raw))
			}

			values = append(values, value)

			continue
		}

		if isChosen {
			values = append(values, protocol.Value{Value: setting.Name + "=", Description: remark(setting.Description, "empty: gives it back")})
		}

		for _, choice := range choices {
			value := protocol.Value{Value: setting.Name + "=" + choice.Value, Label: choice.Label, Description: choice.Description}

			if raw, found := inForce[setting.Name]; found && plainValue(raw) == choice.Value {
				value.Description = remark(value.Description, "in force")
			}

			values = append(values, value)
		}
	}

	return values, nil
}

// offers starts a provider, if it is not running, for what it says it offers.
func (h *host) offers(ctx context.Context, name string) (protocol.Description, *provider.Pool, error) {
	pool, err := h.ready(ctx, name)

	if err != nil {
		return protocol.Description{}, nil, err
	}

	return pool.Description(), pool, nil
}

// choicesOf returns the values an option takes: both for a boolean, the
// fixed ones, or those its function lists. One that takes any value has
// none to list.
func choicesOf(ctx context.Context, asked provider.Provider, option protocol.Option) ([]protocol.Value, error) {
	switch {
	case option.Type == protocol.OptionBoolean:
		return []protocol.Value{{Value: "true"}, {Value: "false"}}, nil

	case len(option.Enum) > 0:
		values := make([]protocol.Value, len(option.Enum))

		for index, value := range option.Enum {
			values[index] = protocol.Value{Value: value}
		}

		return values, nil

	case option.Values != "":
		raw, err := asked.Invoke(ctx, option.Values, nil, unattended{})

		if err != nil {
			return nil, err
		}

		var listed protocol.ValuesResult

		if err := json.Unmarshal(raw, &listed); err != nil {
			return nil, fmt.Errorf("%s did not answer with values: %w", option.Values, err)
		}

		return listed.Values, nil
	}

	return nil, nil
}

// unattended is the dialog of a function the host calls with nobody to
// answer it: what it says is dropped, and what it asks fails.
type unattended struct{}

func (unattended) Say(string) {}

func (unattended) Ask(context.Context, string, bool) (string, error) {
	return "", errors.New("nobody is there to answer while values are listed")
}

// plainValue is an option's value as text: a string without its quotes, and
// anything else as JSON writes it.
func plainValue(raw json.RawMessage) string {
	var text string

	if json.Unmarshal(raw, &text) == nil {
		return text
	}

	return string(raw)
}

// remark puts a mark before a value's description.
func remark(description, mark string) string {
	if description == "" {
		return mark
	}

	return mark + "; " + description
}
