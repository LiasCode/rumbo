package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/LiasCode/rumbo/internal/agent"
	"github.com/LiasCode/rumbo/internal/project"
	"github.com/LiasCode/rumbo/internal/store"
)

// config is what rumbo reads from the system settings when it starts: the
// front end, and the provider a new session starts with.
type config struct {
	Frontend        string
	DefaultProvider string
}

// limits are the system settings a run reads when it starts, so a change
// applies to the runs that start after it.
type limits struct {
	MaxTokens  int
	MaxTurns   int
	Compaction agent.Compaction
}

// configFile is the name of the file that holds what the user chose.
const configFile = "config.yaml"

// ConfigPath is the file that holds what the user chose: RUMBO_CONFIG, or
// config.yaml in rumbo's directory of the user's configuration, next to
// their instructions and skills.
func ConfigPath() (string, error) {
	if path := os.Getenv("RUMBO_CONFIG"); path != "" {
		return path, nil
	}

	dir, err := os.UserConfigDir()

	if err != nil {
		return "", err
	}

	return filepath.Join(dir, "rumbo", configFile), nil
}

// DataDir is where the sessions are kept: RUMBO_DATA, or rumbo's directory
// of the user's data.
func DataDir() (string, error) {
	if dir := os.Getenv("RUMBO_DATA"); dir != "" {
		return dir, nil
	}

	if data := os.Getenv("XDG_DATA_HOME"); data != "" {
		return filepath.Join(data, "rumbo"), nil
	}

	home, err := os.UserHomeDir()

	if err != nil {
		return "", err
	}

	return filepath.Join(home, ".local", "share", "rumbo"), nil
}

// openStore opens the files rumbo keeps: what the user chose, and the
// sessions.
func openStore() (store.Store, error) {
	chosen, err := ConfigPath()

	if err != nil {
		return nil, err
	}

	data, err := DataDir()

	if err != nil {
		return nil, err
	}

	return store.Open(chosen, data), nil
}

// workingProject returns the current directory and the root of the project
// it is part of. Sessions are kept by root.
func workingProject() (dir, root string, err error) {
	dir, err = os.Getwd()

	if err != nil {
		return "", "", err
	}

	return dir, project.Root(context.Background(), dir), nil
}

// loadConfig resolves the system settings rumbo starts with. A malformed
// one is an error; nothing is guessed. Those that runs read later are
// checked too, and so is every name written in the file, so a malformed or
// misspelled one stops rumbo before a front end starts.
func loadConfig(settings store.Store, frontendFlag string) (config, error) {
	loaded := config{Frontend: frontendFlag}

	if err := checkStored(settings); err != nil {
		return loaded, err
	}

	provider, err := effectiveSetting(settings, keyDefaultProvider)

	if err != nil {
		return loaded, err
	}

	loaded.DefaultProvider = provider.Value

	if loaded.Frontend == "" {
		chosen, err := effectiveSetting(settings, keyFrontend)

		if err != nil {
			return loaded, err
		}

		switch {
		case chosen.Source != sourceUnset:
			loaded.Frontend = chosen.Value
		case isTerminal(os.Stdin) && isTerminal(os.Stdout):
			loaded.Frontend = "tui"
		default:
			loaded.Frontend = "plain"
		}
	}

	if _, err := loadLimits(settings); err != nil {
		return loaded, err
	}

	return loaded, nil
}

// loadLimits resolves the system settings a run reads when it starts.
func loadLimits(settings store.Store) (limits, error) {
	var loaded limits
	var err error

	for key, into := range map[string]*int{keyMaxTokens: &loaded.MaxTokens, keyMaxTurns: &loaded.MaxTurns} {
		if *into, err = effectiveCount(settings, key); err != nil {
			return loaded, err
		}
	}

	compactAt, err := effectiveSetting(settings, keyCompactAt)

	if err != nil {
		return loaded, err
	}

	if loaded.Compaction, err = parseCompactAt(compactAt.Value); err != nil {
		return loaded, fmt.Errorf("setting %s: %w", keyCompactAt, err)
	}

	return loaded, nil
}

// effectiveCount reads a numeric setting in force. One that is not set, and
// has no default, is zero.
func effectiveCount(settings store.Store, key string) (int, error) {
	chosen, err := effectiveSetting(settings, key)

	if err != nil || chosen.Source == sourceUnset {
		return 0, err
	}

	number, err := strconv.Atoi(chosen.Value)

	if err != nil || number < 0 {
		return 0, fmt.Errorf("setting %s must be a whole number, zero or more, got %q", key, chosen.Value)
	}

	return number, nil
}
