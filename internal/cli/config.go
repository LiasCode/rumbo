package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/LiasCode/rumbo/internal/agent"
	"github.com/LiasCode/rumbo/internal/project"
	"github.com/LiasCode/rumbo/internal/store"
)

// config is what rumbo reads from the settings when it starts: what the
// front end and the first session are.
type config struct {
	Frontend string
	Provider string
	Model    string
	// Flags are the provider options the command line gave for this start.
	Flags json.RawMessage
}

// limits are the system settings a run reads when it starts, so a change
// applies to the runs that start after it.
type limits struct {
	MaxTokens  int
	MaxTurns   int
	Compaction agent.Compaction
}

// DatabasePath is where the SQLite file lives: RUMBO_DB, or the user's data
// directory.
func DatabasePath() (string, error) {
	if path := os.Getenv("RUMBO_DB"); path != "" {
		return path, nil
	}

	if data := os.Getenv("XDG_DATA_HOME"); data != "" {
		return filepath.Join(data, "rumbo", "rumbo.db"), nil
	}

	home, err := os.UserHomeDir()

	if err != nil {
		return "", err
	}

	return filepath.Join(home, ".local", "share", "rumbo", "rumbo.db"), nil
}

// openStore opens the database and registers the built-in programs.
func openStore() (store.Store, error) {
	path, err := DatabasePath()

	if err != nil {
		return nil, err
	}

	database, err := store.Open(path)

	if err != nil {
		return nil, err
	}

	if err := seedBuiltins(database); err != nil {
		return nil, err
	}

	return database, nil
}

// workingProject returns the current directory and the root of the project
// it is part of. Project settings and sessions are kept by root.
func workingProject() (dir, root string, err error) {
	dir, err = os.Getwd()

	if err != nil {
		return "", "", err
	}

	return dir, project.Root(context.Background(), dir), nil
}

// loadConfig resolves the settings rumbo starts with in the project at
// root. A missing or malformed required setting is an error; nothing is
// guessed. Those that runs read later are checked too, so a malformed one
// stops rumbo before a front end starts.
func loadConfig(settings store.Store, root, modelFlag, frontendFlag string, flagOptions json.RawMessage) (config, error) {
	loaded := config{Frontend: frontendFlag}

	if loaded.Frontend == "" {
		chosen, err := effectiveSetting(settings, root, keyFrontend)

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

	model := modelFlag

	if model == "" {
		chosen, err := effectiveSetting(settings, root, keyModel)

		if err != nil {
			return loaded, err
		}

		model = chosen.Value
	}

	providerName, modelName, found := strings.Cut(model, "/")

	if !found || providerName == "" || modelName == "" {
		return loaded, fmt.Errorf(`no model selected: run "rumbo system set model <provider>/<model>" (for example anthropic/claude-opus-5-5) or pass -m`)
	}

	loaded.Provider = providerName
	loaded.Model = modelName
	loaded.Flags = flagOptions

	if _, err := storedOptions(settings, root, providerName); err != nil {
		return loaded, err
	}

	if _, err := loadLimits(settings, root); err != nil {
		return loaded, err
	}

	return loaded, nil
}

// optionsInForce are the option values a run of provider gets in the
// project at root: those stored for the provider, the ones chosen in the
// session over them, and those of the command line over all.
func optionsInForce(database store.Store, root, provider string, session, flags json.RawMessage) (json.RawMessage, error) {
	stored, err := storedOptions(database, root, provider)

	if err != nil {
		return nil, err
	}

	return mergeOptions(stored, session, flags)
}

// loadLimits resolves the system settings for a run in the project at root.
func loadLimits(settings store.Store, root string) (limits, error) {
	var loaded limits
	var err error

	for key, into := range map[string]*int{keyMaxTokens: &loaded.MaxTokens, keyMaxTurns: &loaded.MaxTurns} {
		if *into, err = effectiveCount(settings, root, key); err != nil {
			return loaded, err
		}
	}

	compactAt, err := effectiveSetting(settings, root, keyCompactAt)

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
func effectiveCount(settings store.Store, root, key string) (int, error) {
	chosen, err := effectiveSetting(settings, root, key)

	if err != nil || chosen.Source == sourceUnset {
		return 0, err
	}

	number, err := strconv.Atoi(chosen.Value)

	if err != nil || number < 0 {
		return 0, fmt.Errorf("setting %s must be a whole number, zero or more, got %q", key, chosen.Value)
	}

	return number, nil
}
