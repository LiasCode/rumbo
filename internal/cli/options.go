package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/LiasCode/rumbo/protocol"
)

// This file owns a provider's options: the values sent to it with every
// request, which belong to the provider and not to the system settings. The
// command line and the host's functions both go through it.

// An option has no place of its own. The value a user sets for a provider
// is a field of the provider's configuration, like any other; a session's
// own values are laid over it, and win in it. Both are JSON objects the host
// forwards, and only the provider knows what they mean.

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

// fieldValue reads the value of a name=value change for a setting nothing
// describes. A value that is valid JSON is kept as it is, so true and 3 are
// a boolean and a number; anything else is text.
func fieldValue(value string) json.RawMessage {
	if json.Valid([]byte(value)) {
		return json.RawMessage(value)
	}

	quoted, _ := json.Marshal(value)

	return quoted
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

// optionsInForce are the option values a run gets with every request: the
// fields of its provider's configuration that the provider describes as
// options, with the ones chosen in the session over them. The configuration
// reaches the provider whole when it starts; sending its options with the
// request too serves a provider that only reads them there.
func optionsInForce(config json.RawMessage, described []protocol.Option, session json.RawMessage) (json.RawMessage, error) {
	fields := map[string]json.RawMessage{}

	if len(config) > 0 {
		if err := json.Unmarshal(config, &fields); err != nil {
			return nil, fmt.Errorf("the provider's configuration must be a JSON object: %w", err)
		}
	}

	options := map[string]json.RawMessage{}

	for _, option := range described {
		if value, chosen := fields[option.Name]; chosen {
			options[option.Name] = value
		}
	}

	var configured json.RawMessage

	if len(options) > 0 {
		configured, _ = json.Marshal(options)
	}

	return mergeOptions(configured, session)
}
