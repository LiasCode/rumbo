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
// object, kept globally or for one project like a system setting. The host
// stores and forwards it; only the provider knows what it means.
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

// object turns the flags into a JSON object.
func (o optionFlags) object() json.RawMessage {
	if len(o) == 0 {
		return nil
	}

	fields := map[string]json.RawMessage{}

	for _, flag := range o {
		name, value, _ := strings.Cut(flag, "=")
		fields[name] = fieldValue(value)
	}

	raw, _ := json.Marshal(fields)

	return raw
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

// storedOptions returns the provider's option values from the settings: the
// global ones with the project's laid over them.
func storedOptions(settings store.Store, root, provider string) (json.RawMessage, error) {
	key := optionsPrefix + provider
	var layers []json.RawMessage

	for _, scope := range []string{store.GlobalScope, root} {
		value, found, err := settings.Setting(scope, key)

		if err != nil {
			return nil, err
		}

		if found {
			layers = append(layers, json.RawMessage(value))
		}
	}

	options, err := mergeOptions(layers...)

	if err != nil {
		return nil, fmt.Errorf("the options stored for %s: %w", provider, err)
	}

	return options, nil
}

// changeOptions changes the option values stored for a provider in one
// scope, and leaves the rest. When none are left there, nothing is stored,
// so the next scope in line applies.
func changeOptions(database store.Store, scope, provider string, changes []string) error {
	if _, err := database.Program(store.KindProvider, provider); err != nil {
		return fmt.Errorf("provider %q is not registered", provider)
	}

	key := optionsPrefix + provider
	fields := map[string]json.RawMessage{}

	stored, found, err := database.Setting(scope, key)

	if err != nil {
		return err
	}

	if found {
		if err := json.Unmarshal([]byte(stored), &fields); err != nil {
			return fmt.Errorf("the options stored for %s are not an object: %w", provider, err)
		}
	}

	if err := applyChanges(fields, changes); err != nil {
		return err
	}

	if len(fields) > 0 {
		raw, _ := json.Marshal(fields)

		return database.SetSetting(scope, key, string(raw))
	}

	if found {
		return database.DeleteSetting(scope, key)
	}

	return nil
}

// describeOptions says which option values a provider gets in a project.
func describeOptions(database store.Store, root, provider string) (string, error) {
	if _, err := database.Program(store.KindProvider, provider); err != nil {
		return "", fmt.Errorf("provider %q is not registered", provider)
	}

	options, err := storedOptions(database, root, provider)

	if err != nil {
		return "", err
	}

	if len(options) == 0 {
		return fmt.Sprintf("no options are stored for %s; it uses its own defaults", provider), nil
	}

	return fmt.Sprintf("options of %s in force here: %s", provider, options), nil
}
