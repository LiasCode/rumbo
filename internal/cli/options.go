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

// A provider's options are kept with its registration, a JSON object the
// host stores and forwards; only the provider knows what it means. A
// session's own values are laid over them, and win in it.

// optionsPrefix started the setting that held a provider's options before
// they were kept with its registration: "options.anthropic", for example.
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
func storedOptions(database store.Store, provider string) (json.RawMessage, error) {
	program, err := database.Program(store.KindProvider, provider)

	if err != nil {
		return nil, fmt.Errorf("provider %q is not registered", provider)
	}

	options, err := mergeOptions(program.Options)

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
// leaves the rest. One removed goes back to the provider's default the next
// time the provider says it.
func changeOptions(database store.Store, provider string, changes []string) error {
	program, err := database.Program(store.KindProvider, provider)

	if err != nil {
		return fmt.Errorf("provider %q is not registered", provider)
	}

	if program.Options, err = changeObject(program.Options, changes); err != nil {
		return err
	}

	return database.PutProgram(store.KindProvider, program)
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
