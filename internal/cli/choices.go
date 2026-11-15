package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LiasCode/rumbo/internal/provider"
	"github.com/LiasCode/rumbo/protocol"
)

// What a provider can be given is known only to the provider: its models,
// and the values its options take. These functions ask it, so a front end
// can offer them as choices for the functions that take them: use for a
// model, and session-options and provider-options for a name=value change.

// providerModels lists the models a provider offers: the one named, or the
// current session's, with the one this session uses marked.
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

	values, err := choicesOf(ctx, pool, *described.Model)

	if err != nil {
		return protocol.HostResult{}, fmt.Errorf("the models of %s: %w", name, err)
	}

	for index, value := range values {
		if name == chosen.provider && value.Value == chosen.model {
			values[index].Label = marked(value.Label, "in use in this session")
		}
	}

	return protocol.HostResult{Text: "the models " + name + " offers:", Values: values}, nil
}

// optionValues lists the changes the options of a provider can take, as
// name=value: the one named, with the values stored for it marked, or the
// current session's, with the values in force in it marked. The empty value
// of each option comes first, which removes what was chosen.
func (h *host) optionValues(ctx context.Context, params map[string]string) (protocol.HostResult, error) {
	h.mu.Lock()
	chosen := h.current.choice
	h.mu.Unlock()

	name := params["provider"]

	var current json.RawMessage
	var err error

	if name == "" {
		name = chosen.provider
		current, err = h.inForce(chosen)
	} else {
		current, err = storedOptions(h.database, name)
	}

	if err != nil {
		return protocol.HostResult{}, err
	}

	var inForce map[string]json.RawMessage

	_ = json.Unmarshal(current, &inForce)

	described, pool, err := h.offers(ctx, name)

	if err != nil {
		return protocol.HostResult{}, err
	}

	var values []protocol.Value

	for _, option := range described.Options {
		values = append(values, protocol.Value{Value: option.Name + "=", Label: "empty: removes it", Description: option.Description})

		choices, err := choicesOf(ctx, pool, option)

		if err != nil {
			return protocol.HostResult{}, fmt.Errorf("the values of %s: %w", option.Name, err)
		}

		for _, choice := range choices {
			value := protocol.Value{Value: option.Name + "=" + choice.Value, Label: choice.Label, Description: choice.Description}

			if raw, found := inForce[option.Name]; found && plainValue(raw) == choice.Value {
				value.Label = marked(value.Label, "in force")
			}

			values = append(values, value)
		}
	}

	if len(values) == 0 {
		return protocol.HostResult{Text: name + " takes no options"}, nil
	}

	return protocol.HostResult{Text: "the values the options of " + name + " take:", Values: values}, nil
}

// offers starts a provider, if it is not running, for what it says it offers.
func (h *host) offers(ctx context.Context, name string) (protocol.Description, *provider.Pool, error) {
	pool, err := h.pool(name)

	if err != nil {
		return protocol.Description{}, nil, err
	}

	if err := pool.Ready(ctx); err != nil {
		return protocol.Description{}, nil, err
	}

	return pool.Description(), pool, nil
}

// choicesOf returns the values an option takes: both for a boolean, the
// fixed ones, or those its function lists. One that takes any value has
// none to list.
func choicesOf(ctx context.Context, pool *provider.Pool, option protocol.Option) ([]protocol.Value, error) {
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
		raw, err := pool.Invoke(ctx, option.Values, nil, unattended{})

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

// marked adds a mark to a label.
func marked(label, mark string) string {
	if label == "" {
		return mark
	}

	return label + ", " + mark
}
