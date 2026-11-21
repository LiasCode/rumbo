package cli

import (
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
// there. Those the user chose are under system in the configuration file;
// the others have their default, or none. The command line and the host's
// functions both go through this file, so a setting behaves the same
// whoever changes it.

// System setting keys.
const (
	keyDefaultProvider = "default_provider"
	keyFrontend        = "frontend"
	keyMaxTurns        = "max_turns"
	keyMaxTokens       = "max_tokens"
	keyCompactAt       = "compact_at"
	keyComputer        = "computer"
)

// Where a value in force comes from: the configuration file, rumbo's own
// default, how rumbo has a built-in program, or the session that chose it.
// A value that comes from nowhere is not set.
const (
	sourceFile    = configFile
	sourceDefault = "default"
	sourceBuiltin = "built in"
	sourceSession = "session"
	sourceUnset   = "not set"
)

// settingSpec describes one setting.
type settingSpec struct {
	key  string
	help string
	// fallback is its default; unset says what applies when it has none.
	fallback string
	unset    string
	check    func(database store.Store, value string) error
	// atStart says rumbo reads the setting when it starts. The others are
	// read when a run starts.
	atStart bool
}

var settingSpecs = []settingSpec{
	{key: keyDefaultProvider, help: "provider a new session starts with, with the provider's model", unset: "-p chooses one", check: checkProvider, atStart: true},
	{key: keyFrontend, help: "front end to start", unset: "tui in a terminal, plain otherwise", check: checkFrontend, atStart: true},
	{key: keyMaxTurns, help: "model responses after which a run is stopped; 0 for no limit", fallback: "150", check: checkCount},
	{key: keyMaxTokens, help: "output token limit per response", unset: "the provider's own limit", check: checkCount},
	{key: keyCompactAt, help: "share of the model's context at which the conversation is summarized, such as 80%; 0% never; 150000 tokens when the provider does not say the context's size", fallback: "80%", check: checkCompactAt},
	{key: keyComputer, help: "on gives the model the computer tool: it sees the screen and uses the pointer and the keyboard of the desktop rumbo runs in", fallback: "off", check: checkSwitch},
}

// retiredSettings are the system settings there were, and what took their
// place.
var retiredSettings = map[string]string{
	"default_model": `the model is now each provider's own: "rumbo provider set <provider> model=<model>"`,
	"model":         `the model is now each provider's own: "rumbo provider set <provider> model=<model>"`,
}

func checkProvider(database store.Store, value string) error {
	if _, err := findProgram(database, store.KindProvider, value); err != nil {
		return err
	}

	return nil
}

func checkFrontend(database store.Store, value string) error {
	if _, err := findProgram(database, store.KindFrontend, value); err != nil {
		return err
	}

	return nil
}

func checkCount(_ store.Store, value string) error {
	if number, err := strconv.Atoi(value); err != nil || number < 0 {
		return fmt.Errorf("it takes a whole number, zero or more, not %q", value)
	}

	return nil
}

func checkSwitch(_ store.Store, value string) error {
	if value != "on" && value != "off" {
		return fmt.Errorf("it takes on or off, not %q", value)
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
		return agent.Compaction{}, fmt.Errorf(`it takes a share of the model's context between 0%% and 100%%, such as 80%%, or 0%% for never, not %q; set it with "rumbo system set compact_at=80%%"`, value)
	}

	if percent == 0 {
		return agent.Compaction{Never: true}, nil
	}

	return agent.Compaction{Percent: percent}, nil
}

// applies says when a change to the setting takes effect.
func (spec settingSpec) applies() string {
	if spec.atStart {
		return "from the next start of rumbo"
	}

	return "to the runs that start from now on"
}

// findSetting returns the system setting called key, or says why there is
// none.
func findSetting(key string) (settingSpec, error) {
	for _, spec := range settingSpecs {
		if spec.key == key {
			return spec, nil
		}
	}

	if instead, retired := retiredSettings[key]; retired {
		return settingSpec{}, fmt.Errorf("there is no system setting %s any more: %s", key, instead)
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

// setSettings makes changes to the system settings, each name=value, where
// an empty value gives the setting back to its default. Every change is
// checked before any is stored. It returns a line saying each change, and
// when it applies.
func setSettings(database store.Store, written []string) ([]string, error) {
	changes, err := parseChanges(written)

	if err != nil {
		return nil, err
	}

	stored, err := database.Settings()

	if err != nil {
		return nil, err
	}

	said := make([]string, len(changes))

	for index, each := range changes {
		spec, err := findSetting(each.name)

		if err != nil {
			return nil, err
		}

		if each.back() {
			if _, found := stored[each.name]; !found {
				return nil, fmt.Errorf("%s is not set in %s, so there is nothing to give back", each.name, configFile)
			}

			said[index] = each.name + ": given back to " + spec.fallbackText() + ", " + spec.applies()

			continue
		}

		if err := checkSetting(database, each.name, each.value); err != nil {
			return nil, err
		}

		said[index] = each.name + " = " + each.value + ", " + spec.applies()
	}

	for _, each := range changes {
		if each.back() {
			err = database.DeleteSetting(each.name)
		} else {
			err = database.SetSetting(each.name, each.value)
		}

		if err != nil {
			return nil, err
		}
	}

	return said, nil
}

// fallbackText says what a setting is when it is not set.
func (spec settingSpec) fallbackText() string {
	if spec.fallback != "" {
		return "its default, " + spec.fallback
	}

	return "its default: " + spec.unset
}

// checkStored refuses a name under system in the configuration file that
// is no system setting, which is how a misspelled one shows.
func checkStored(database store.Store) error {
	stored, err := database.Settings()

	if err != nil {
		return err
	}

	for key := range stored {
		if _, err := findSetting(key); err != nil {
			return fmt.Errorf("%s, under system: %w", configFile, err)
		}
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

// effectiveSetting finds the value in force: the one in the configuration
// file, else the default.
func effectiveSetting(database store.Store, key string) (effective, error) {
	value, found, err := database.Setting(key)

	if err != nil {
		return effective{}, err
	}

	if found {
		return effective{Key: key, Value: value, Source: sourceFile}, nil
	}

	for _, spec := range settingSpecs {
		if spec.key == key && spec.fallback != "" {
			return effective{Key: key, Value: spec.fallback, Source: sourceDefault}, nil
		}
	}

	return effective{Key: key, Source: sourceUnset}, nil
}

// effectiveSettings returns every system setting as it applies. A name
// in the file that is no setting is said, not left out.
func effectiveSettings(database store.Store) ([]effective, error) {
	if err := checkStored(database); err != nil {
		return nil, err
	}

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
