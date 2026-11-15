package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/LiasCode/rumbo/internal/store"
)

// This file owns a provider's options: the values sent to it with every
// request, which belong to the provider and not to the system settings. The
// command line and the host's functions both go through it.

// optionsPrefix starts the stored key that holds the option values sent to
// one provider: "options.anthropic", for example. Its value is a JSON
// object. The host stores and forwards it; only the provider knows what it
// means.
//
// A session's own values are laid over these, and win in it.
const optionsPrefix = "options."

// optionFlags collects the -o name=value flags of the command line.
type optionFlags []string

func (o *optionFlags) String() string {
	return strings.Join(*o, ",")
}

func (o *optionFlags) Set(value string) error {
	if name, _, found := strings.Cut(value, "="); !found || name == "" {
		return errors.New("expected name=value")
	}

	*o = append(*o, value)

	return nil
}

// fieldValue reads the value of a name=value change. A value that is valid
// JSON is kept as it is, so true and 3 are a boolean and a number; anything
// else is text.
func fieldValue(value string) json.RawMessage {
	if json.Valid([]byte(value)) {
		return json.RawMessage(value)
	}

	quoted, _ := json.Marshal(value)

	return quoted
}

// applyChanges lays name=value changes over the fields of an object and
// leaves the others. An empty value removes the field.
func applyChanges(fields map[string]json.RawMessage, changes []string) error {
	for _, change := range changes {
		name, value, found := strings.Cut(change, "=")

		if !found || name == "" {
			return fmt.Errorf("%q is not name=value", change)
		}

		if value == "" {
			delete(fields, name)
			continue
		}

		fields[name] = fieldValue(value)
	}

	return nil
}

// mergeOptions lays each JSON object over the ones before it, field by
// field. Empty ones are skipped. The result has its fields in a fixed order,
// so the same choices always produce the same bytes.
func mergeOptions(layers ...json.RawMessage) (json.RawMessage, error) {
	merged := map[string]json.RawMessage{}

	for _, layer := range layers {
		if len(layer) == 0 {
			continue
		}

		var fields map[string]json.RawMessage

		if err := json.Unmarshal(layer, &fields); err != nil {
			return nil, fmt.Errorf("options must be a JSON object: %w", err)
		}

		for name, value := range fields {
			merged[name] = value
		}
	}

	if len(merged) == 0 {
		return nil, nil
	}

	return json.Marshal(merged)
}

// storedOptions returns the option values stored for the provider.
func storedOptions(settings store.Store, provider string) (json.RawMessage, error) {
	value, _, err := settings.Setting(store.GlobalScope, optionsPrefix+provider)

	if err != nil {
		return nil, err
	}

	options, err := mergeOptions(json.RawMessage(value))

	if err != nil {
		return nil, fmt.Errorf("the options stored for %s: %w", provider, err)
	}

	return options, nil
}

// changeObject lays name=value changes over the option values in options,
// as applyChanges does. What is left is nil when no value is.
func changeObject(options json.RawMessage, changes []string) (json.RawMessage, error) {
	fields := map[string]json.RawMessage{}

	if len(options) > 0 {
		if err := json.Unmarshal(options, &fields); err != nil {
			return nil, fmt.Errorf("the options are not an object: %w", err)
		}
	}

	if err := applyChanges(fields, changes); err != nil {
		return nil, err
	}

	if len(fields) == 0 {
		return nil, nil
	}

	return json.Marshal(fields)
}

// changeOptions changes the option values stored for a provider, and
// leaves the rest. When none are left, nothing is stored.
func changeOptions(database store.Store, provider string, changes []string) error {
	if _, err := database.Program(store.KindProvider, provider); err != nil {
		return fmt.Errorf("provider %q is not registered", provider)
	}

	key := optionsPrefix + provider

	stored, found, err := database.Setting(store.GlobalScope, key)

	if err != nil {
		return err
	}

	changed, err := changeObject(json.RawMessage(stored), changes)

	if err != nil {
		return err
	}

	if changed != nil {
		return database.SetSetting(store.GlobalScope, key, string(changed))
	}

	if found {
		return database.DeleteSetting(store.GlobalScope, key)
	}

	return nil
}

// describeOptions says which option values are stored for a provider.
func describeOptions(database store.Store, provider string) (string, error) {
	if _, err := database.Program(store.KindProvider, provider); err != nil {
		return "", fmt.Errorf("provider %q is not registered", provider)
	}

	options, err := storedOptions(database, provider)

	if err != nil {
		return "", err
	}

	if len(options) == 0 {
		return fmt.Sprintf("no options are stored for %s; it uses its own defaults", provider), nil
	}

	return fmt.Sprintf("options of %s: %s", provider, options), nil
}
