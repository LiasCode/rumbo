package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/LiasCode/rumbo/internal/store"
)

// optionsPrefix starts the setting that holds the option values sent to one
// provider: "options.anthropic", for example. Its value is a JSON object.
// The host stores and forwards it; only the provider knows what it means.
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

// object turns the flags into a JSON object. A value that is valid JSON is
// kept as it is, so true and 3 are a boolean and a number; anything else is
// a string.
func (o optionFlags) object() json.RawMessage {
	if len(o) == 0 {
		return nil
	}

	fields := map[string]json.RawMessage{}

	for _, flag := range o {
		name, value, _ := strings.Cut(flag, "=")

		if !json.Valid([]byte(value)) {
			quoted, _ := json.Marshal(value)
			value = string(quoted)
		}

		fields[name] = json.RawMessage(value)
	}

	raw, _ := json.Marshal(fields)

	return raw
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
		return nil, fmt.Errorf("setting %s: %w", key, err)
	}

	return options, nil
}
