package cli

import (
	"encoding/json"
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
// provider, and what a session chooses for itself is kept with it and wins
// there. The command line and the host's functions both go through this
// file, so a setting behaves the same whoever changes it.

// System setting keys.
const (
	keyDefaultProvider = "default_provider"
	keyDefaultModel    = "default_model"
	keyFrontend        = "frontend"
	keyMaxTurns        = "max_turns"
	keyMaxTokens       = "max_tokens"
	keyCompactAt       = "compact_at"
)

// legacyModel is the setting that held both the provider and the model, as
// "provider/model", before they were two.
const legacyModel = "model"

// internalPrefix starts the keys rumbo keeps for itself. They are not
// settings a user reads or writes.
const internalPrefix = "_"

// Where a value in force comes from.
const (
	sourceStored  = "stored"
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
	{key: keyDefaultProvider, help: "provider a new session starts with", check: checkProvider, atStart: true},
	{key: keyDefaultModel, help: "model a new session starts with, of the default provider", check: checkModel, atStart: true},
	{key: keyFrontend, help: "front end to start; unset uses tui in a terminal and plain otherwise", check: checkFrontend, atStart: true},
	{key: keyMaxTurns, help: "model responses after which a run is stopped; 0 for no limit", fallback: "150", check: checkCount},
	{key: keyMaxTokens, help: "output token limit per response; unset uses the provider's default", check: checkCount},
	{key: keyCompactAt, help: "share of the model's context at which the conversation is summarized, such as 80%; 0% never; 150000 tokens when the provider does not say the context's size", fallback: "80%", check: checkCompactAt},
}

func checkProvider(database store.Store, value string) error {
	if _, err := database.Program(store.KindProvider, value); err != nil {
		return fmt.Errorf(`provider %q is not registered; see "rumbo provider list"`, value)
	}

	return nil
}

// checkModel only asks for a name: what models there are is the provider's
// to say, and a name may hold a slash, as many servers' names do.
func checkModel(_ store.Store, value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("it takes the name of a model")
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

// parseCompactAt reads the compact_at setting: a share of the model's
// context, written with a percent sign, where 0% is never. An empty value is
// the default.
func parseCompactAt(value string) (agent.Compaction, error) {
	if value == "" {
		return agent.Compaction{}, nil
	}

	share, found := strings.CutSuffix(value, "%")
	percent, err := strconv.Atoi(share)

	if !found || err != nil || percent < 0 || percent > 100 {
		return agent.Compaction{}, fmt.Errorf(`it takes a share of the model's context between 0%% and 100%%, such as 80%%, or 0%% for never, not %q; set it with "rumbo system set compact_at 80%%"`, value)
	}

	if percent == 0 {
		return agent.Compaction{Never: true}, nil
	}

	return agent.Compaction{Percent: percent}, nil
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

	if key == legacyModel {
		return settingSpec{}, fmt.Errorf("%s is now two settings: %s and %s", legacyModel, keyDefaultProvider, keyDefaultModel)
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

// setSetting stores a value after checking it.
func setSetting(database store.Store, key, value string) error {
	if err := checkSetting(database, key, value); err != nil {
		return err
	}

	return database.SetSetting(store.GlobalScope, key, value)
}

// storedSetting returns the value stored for a system setting.
func storedSetting(database store.Store, key string) (string, error) {
	if _, err := findSetting(key); err != nil {
		return "", err
	}

	value, found, err := database.Setting(store.GlobalScope, key)

	if err == nil && !found {
		err = fmt.Errorf("%s is not set", key)
	}

	return value, err
}

// unsetSetting removes a stored value, so the default applies.
func unsetSetting(database store.Store, key string) error {
	if _, err := findSetting(key); err != nil {
		return err
	}

	if err := database.DeleteSetting(store.GlobalScope, key); errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%s is not set", key)
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

// effective is the value of a setting in force, and where it comes from.
type effective struct {
	Key    string
	Value  string
	Source string
}

// effectiveSetting finds the value in force: the stored one, else the
// default.
func effectiveSetting(database store.Store, key string) (effective, error) {
	value, found, err := database.Setting(store.GlobalScope, key)

	if err != nil {
		return effective{}, err
	}

	if found {
		return effective{Key: key, Value: value, Source: sourceStored}, nil
	}

	for _, spec := range settingSpecs {
		if spec.key == key && spec.fallback != "" {
			return effective{Key: key, Value: spec.fallback, Source: sourceDefault}, nil
		}
	}

	return effective{Key: key, Source: sourceUnset}, nil
}

// effectiveSettings returns every system setting as it applies.
func effectiveSettings(database store.Store) ([]effective, error) {
	settings := make([]effective, len(settingSpecs))

	for index, spec := range settingSpecs {
		setting, err := effectiveSetting(database, spec.key)

		if err != nil {
			return nil, err
		}

		settings[index] = setting
	}

	return settings, nil
}

// ignoredSettings are the values stored for one project, which rumbo kept
// before its settings became the same everywhere. They are left where they
// are and no longer read.
func ignoredSettings(database store.Store) ([]store.Setting, error) {
	stored, err := database.Settings()

	if err != nil {
		return nil, err
	}

	var ignored []store.Setting

	for _, setting := range stored {
		if setting.Scope != store.GlobalScope && !strings.HasPrefix(setting.Key, internalPrefix) {
			ignored = append(ignored, setting)
		}
	}

	return ignored, nil
}

// describeSettings renders settings for a person, and the values stored for
// one project that are no longer read, so they do not go unnoticed.
func describeSettings(settings []effective, ignored []store.Setting) string {
	lines := make([]string, 0, len(settings)+len(ignored)+1)

	for _, setting := range settings {
		if setting.Source == sourceUnset {
			lines = append(lines, fmt.Sprintf("%-20s (not set)", setting.Key))
			continue
		}

		lines = append(lines, fmt.Sprintf("%-20s %s  (%s)", setting.Key, setting.Value, setting.Source))
	}

	if len(ignored) > 0 {
		lines = append(lines, "stored for one project, and no longer read; settings are the same everywhere now:")
	}

	for _, setting := range ignored {
		lines = append(lines, fmt.Sprintf("  %s = %s  (%s)", setting.Key, setting.Value, setting.Scope))
	}

	return strings.Join(lines, "\n")
}

// keyUpgraded records how far the settings stored by an earlier version
// were carried over to the ones that replace them, as the number of the
// last step taken.
const keyUpgraded = internalPrefix + "settings_upgraded"

// upgrades carry what an earlier version stored over to what replaces it,
// each once, in order. What was stored stays as it was, unread.
var upgrades = []func(database store.Store) error{
	// 1: the model, which held the provider too, becomes the default
	// provider and model.
	func(database store.Store) error {
		model, found, err := database.Setting(store.GlobalScope, legacyModel)

		if err != nil {
			return err
		}

		_, chosen, err := database.Setting(store.GlobalScope, keyDefaultProvider)

		if err != nil {
			return err
		}

		provider, name, split := strings.Cut(model, "/")

		if !found || !split || chosen {
			return nil
		}

		for key, value := range map[string]string{keyDefaultProvider: provider, keyDefaultModel: name} {
			if err := database.SetSetting(store.GlobalScope, key, value); err != nil {
				return err
			}
		}

		return nil
	},

	// 2: a provider's options, a setting of their own, are kept with its
	// registration.
	func(database store.Store) error {
		programs, err := database.Programs(store.KindProvider)

		if err != nil {
			return err
		}

		for _, program := range programs {
			options, found, err := database.Setting(store.GlobalScope, optionsPrefix+program.Name)

			if err != nil {
				return err
			}

			if !found || len(program.Options) > 0 {
				continue
			}

			program.Options = json.RawMessage(options)

			if err := database.PutProgram(store.KindProvider, program); err != nil {
				return err
			}
		}

		return nil
	},
}

// upgradeSettings takes the upgrades not taken yet.
func upgradeSettings(database store.Store) error {
	value, _, err := database.Setting(store.GlobalScope, keyUpgraded)

	if err != nil {
		return err
	}

	done, _ := strconv.Atoi(value)

	for step := done; step < len(upgrades); step++ {
		if err := upgrades[step](database); err != nil {
			return err
		}

		if err := database.SetSetting(store.GlobalScope, keyUpgraded, strconv.Itoa(step+1)); err != nil {
			return err
		}
	}

	return nil
}
