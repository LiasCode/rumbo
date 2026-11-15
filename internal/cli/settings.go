package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/LiasCode/rumbo/internal/agent"
	"github.com/LiasCode/rumbo/internal/store"
)

// This file owns what a system setting is: which ones exist, what they
// default to, what values they take, and how the value in force is found.
// System settings are rumbo's own, the same whatever the session and the
// provider; what a session or a provider chooses is kept with it. The
// command line and the host's functions both go through this file, so a
// setting behaves the same whoever changes it.

// System setting keys. A project value overrides the global one.
const (
	keyModel     = "model"
	keyFrontend  = "frontend"
	keyMaxTokens = "max_tokens"
	keyMaxTurns  = "max_turns"
	keyCompactAt = "compact_at"
)

// internalPrefix starts the keys rumbo keeps for itself. They are not
// settings a user reads or writes.
const internalPrefix = "_"

// Where a value in force comes from.
const (
	sourceProject = "project"
	sourceGlobal  = "global"
	sourceDefault = "default"
	sourceUnset   = "not set"
)

// settingSpec describes one setting.
type settingSpec struct {
	key      string
	help     string
	fallback string
	check    func(database store.Store, value string) error
	// atStart says rumbo reads the setting when it starts. The others are
	// read when a run starts.
	atStart bool
}

var settingSpecs = []settingSpec{
	{key: keyModel, help: `provider and model a new session starts with, as "provider/model"`, check: checkModel, atStart: true},
	{key: keyFrontend, help: "front end to start; unset uses tui in a terminal and plain otherwise", check: checkFrontend, atStart: true},
	{key: keyMaxTokens, help: "output token limit per response; unset uses the provider's default", check: checkCount},
	{key: keyMaxTurns, help: "model responses after which a run is stopped; 0 for no limit", fallback: "150", check: checkCount},
	{key: keyCompactAt, help: "when the conversation is summarized: tokens, or a share of the model's context such as 80%; 0 never; unset uses 80%, at most 150000 tokens", check: checkCompactAt},
}

func checkModel(database store.Store, value string) error {
	provider, model, found := strings.Cut(value, "/")

	if !found || provider == "" || model == "" {
		return errors.New(`a model is written "provider/model", for example anthropic/claude-opus-5-5`)
	}

	if _, err := database.Program(store.KindProvider, provider); err != nil {
		return fmt.Errorf(`provider %q is not registered; see "rumbo provider list"`, provider)
	}

	return nil
}

func checkFrontend(database store.Store, value string) error {
	if _, err := database.Program(store.KindFrontend, value); err != nil {
		return fmt.Errorf(`front end %q is not registered; see "rumbo frontend list"`, value)
	}

	return nil
}

func checkCount(_ store.Store, value string) error {
	if number, err := strconv.Atoi(value); err != nil || number < 0 {
		return fmt.Errorf("it takes a whole number, zero or more, not %q", value)
	}

	return nil
}

func checkCompactAt(_ store.Store, value string) error {
	_, err := parseCompactAt(value)

	return err
}

// parseCompactAt reads the compact_at setting: a number of tokens, a share
// of the model's context written with a percent sign, or zero for never. An
// empty value is the default.
func parseCompactAt(value string) (agent.Compaction, error) {
	if value == "" {
		return agent.Compaction{}, nil
	}

	if share, found := strings.CutSuffix(value, "%"); found {
		percent, err := strconv.Atoi(share)

		if err != nil || percent < 1 || percent > 100 {
			return agent.Compaction{}, fmt.Errorf("a share of the context is between 1%% and 100%%, not %q", value)
		}

		return agent.Compaction{Percent: percent}, nil
	}

	tokens, err := strconv.ParseInt(value, 10, 64)

	if err != nil || tokens < 0 {
		return agent.Compaction{}, fmt.Errorf("it takes a number of tokens, a share such as 80%%, or 0 for never, not %q", value)
	}

	if tokens == 0 {
		return agent.Compaction{Never: true}, nil
	}

	return agent.Compaction{Tokens: tokens}, nil
}

// applies says when a change to the setting takes effect.
func (spec settingSpec) applies() string {
	if spec.atStart {
		return "it applies from the next start of rumbo"
	}

	return "it applies to the runs that start from now on"
}

// findSetting returns the system setting called key, or says why there is
// none.
func findSetting(key string) (settingSpec, error) {
	for _, spec := range settingSpecs {
		if spec.key == key {
			return spec, nil
		}
	}

	if provider, found := strings.CutPrefix(key, optionsPrefix); found && provider != "" {
		return settingSpec{}, fmt.Errorf(`%s is not a system setting: a provider's options are its own; change them with provider-options, or "rumbo provider options %s <name>=<value>"`, key, provider)
	}

	return settingSpec{}, fmt.Errorf("there is no system setting %q. The system settings are:\n%s", key, settingsHelp())
}

// checkSetting says why value cannot be stored under key, if it cannot.
func checkSetting(database store.Store, key, value string) error {
	spec, err := findSetting(key)

	if err != nil {
		return err
	}

	if err := spec.check(database, value); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}

	return nil
}

// setSetting stores a value after checking it. An empty scope is global.
func setSetting(database store.Store, scope, key, value string) error {
	if err := checkSetting(database, key, value); err != nil {
		return err
	}

	return database.SetSetting(scope, key, value)
}

// storedSetting returns the value stored for a system setting in one scope.
func storedSetting(database store.Store, scope, key string) (string, error) {
	if _, err := findSetting(key); err != nil {
		return "", err
	}

	value, found, err := database.Setting(scope, key)

	if err == nil && !found {
		err = fmt.Errorf("%s is not set there", key)
	}

	return value, err
}

// unsetSetting removes a stored value, so the next one in line applies.
func unsetSetting(database store.Store, scope, key string) error {
	if _, err := findSetting(key); err != nil {
		return err
	}

	if err := database.DeleteSetting(scope, key); errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%s is not set there", key)
	} else if err != nil {
		return err
	}

	return nil
}

// settingsHelp lists the system settings and what each is for.
func settingsHelp() string {
	lines := make([]string, len(settingSpecs))

	for index, spec := range settingSpecs {
		lines[index] = fmt.Sprintf("  %-20s %s", spec.key, spec.help)
	}

	return strings.Join(lines, "\n")
}

// effective is the value of a setting in force for a project, and where it
// comes from.
type effective struct {
	Key    string
	Value  string
	Source string
}

// effectiveSetting finds the value in force: the project's, else the global
// one, else the default.
func effectiveSetting(database store.Store, root, key string) (effective, error) {
	for _, place := range []struct{ scope, source string }{{root, sourceProject}, {store.GlobalScope, sourceGlobal}} {
		value, found, err := database.Setting(place.scope, key)

		if err != nil {
			return effective{}, err
		}

		if found {
			return effective{Key: key, Value: value, Source: place.source}, nil
		}
	}

	for _, spec := range settingSpecs {
		if spec.key == key && spec.fallback != "" {
			return effective{Key: key, Value: spec.fallback, Source: sourceDefault}, nil
		}
	}

	return effective{Key: key, Source: sourceUnset}, nil
}

// effectiveSettings returns every system setting as it applies to a
// project.
func effectiveSettings(database store.Store, root string) ([]effective, error) {
	settings := make([]effective, len(settingSpecs))

	for index, spec := range settingSpecs {
		setting, err := effectiveSetting(database, root, spec.key)

		if err != nil {
			return nil, err
		}

		settings[index] = setting
	}

	return settings, nil
}

// describeSettings renders settings for a person.
func describeSettings(settings []effective) string {
	lines := make([]string, len(settings))

	for index, setting := range settings {
		if setting.Source == sourceUnset {
			lines[index] = fmt.Sprintf("%-20s (not set)", setting.Key)
			continue
		}

		lines[index] = fmt.Sprintf("%-20s %s  (%s)", setting.Key, setting.Value, setting.Source)
	}

	return strings.Join(lines, "\n")
}
