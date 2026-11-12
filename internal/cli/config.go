package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"rumbo/internal/project"
	"rumbo/internal/store"
	"rumbo/internal/verify"
)

// Setting keys. A project value overrides the global one.
const (
	keyModel           = "model"
	keyFrontend        = "frontend"
	keyChecks          = "checks"
	keyMaxTokens       = "max_tokens"
	keyMaxVerifyRounds = "max_verify_rounds"
	keyMaxTurns        = "max_turns"
	keyCompactAt       = "compact_at"
	keyCheckTimeout    = "check_timeout"
)

var settingHelp = map[string]string{
	keyModel:           `provider and model, as "provider/model"`,
	keyFrontend:        `front end to start; unset uses tui in a terminal and plain otherwise`,
	keyChecks:          `JSON array of commands run when the agent stops; [] disables them; unset detects them`,
	keyMaxTokens:       `output token limit per response; unset uses the provider's default`,
	keyMaxVerifyRounds: `times the agent is sent back to fix failing checks (default 3)`,
	keyMaxTurns:        `model responses after which a run is stopped (default 150; 0 for no limit)`,
	keyCheckTimeout:    `seconds each check may take (default 600)`,
	keyCompactAt:       `context size in tokens at which the conversation is summarized (default 150000; 0 never)`,
}

// selfPlaceholder in a registered command stands for the rumbo executable, so
// built-in programs keep working wherever the binary is installed.
const selfPlaceholder = "{self}"

// config is everything a run reads from settings, resolved once.
type config struct {
	Frontend        string
	Provider        string
	Model           string
	Checks          []string
	ChecksDir       string
	CheckTimeout    int
	MaxTokens       int
	MaxVerifyRounds int
	MaxTurns        int
	CompactAt       int
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

// builtins are the programs served by the rumbo executable itself, by kind,
// with the subcommand that serves them.
var builtins = []struct {
	kind       string
	subcommand string
	names      []string
}{
	{store.KindProvider, "provide", []string{"anthropic", "openai"}},
	{store.KindFrontend, "attach", []string{"tui", "plain"}},
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

	for _, builtin := range builtins {
		for _, name := range builtin.names {
			if _, err := database.Program(builtin.kind, name); errors.Is(err, store.ErrNotFound) {
				program := store.Program{Name: name, Command: []string{selfPlaceholder, builtin.subcommand, name}}

				if err := database.PutProgram(builtin.kind, program); err != nil {
					return nil, err
				}
			}
		}
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

// loadConfig resolves the settings for a run in dir, a directory of the
// project at root. A missing or malformed required setting is an error;
// nothing is guessed.
func loadConfig(settings store.Store, dir, root, modelFlag, frontendFlag string) (config, error) {
	loaded := config{MaxVerifyRounds: 3, MaxTurns: 150, CompactAt: 150_000, CheckTimeout: 600, Frontend: frontendFlag}

	if loaded.Frontend == "" {
		value, found, err := lookup(settings, root, keyFrontend)

		if err != nil {
			return loaded, err
		}

		switch {
		case found:
			loaded.Frontend = value
		case isTerminal(os.Stdin) && isTerminal(os.Stdout):
			loaded.Frontend = "tui"
		default:
			loaded.Frontend = "plain"
		}
	}

	model := modelFlag

	if model == "" {
		value, _, err := lookup(settings, root, keyModel)

		if err != nil {
			return loaded, err
		}

		model = value
	}

	providerName, modelName, found := strings.Cut(model, "/")

	if !found || providerName == "" || modelName == "" {
		return loaded, fmt.Errorf(`no model selected: run "rumbo config set model <provider>/<model>" (for example anthropic/claude-opus-5-5) or pass -m`)
	}

	loaded.Provider = providerName
	loaded.Model = modelName

	checks, found, err := lookup(settings, root, keyChecks)

	if err != nil {
		return loaded, err
	}

	// Checks written by the user are the project's, and run at its root.
	// Detected ones run where the files that define them are.
	if found {
		loaded.ChecksDir = root

		if err := json.Unmarshal([]byte(checks), &loaded.Checks); err != nil {
			return loaded, fmt.Errorf("setting %s must be a JSON array of commands: %w", keyChecks, err)
		}
	} else {
		loaded.Checks, loaded.ChecksDir = verify.Detect(dir, root)
	}

	if loaded.CheckTimeout, err = lookupInt(settings, root, keyCheckTimeout, loaded.CheckTimeout); err != nil {
		return loaded, err
	}

	if loaded.MaxTokens, err = lookupInt(settings, root, keyMaxTokens, loaded.MaxTokens); err != nil {
		return loaded, err
	}

	if loaded.MaxVerifyRounds, err = lookupInt(settings, root, keyMaxVerifyRounds, loaded.MaxVerifyRounds); err != nil {
		return loaded, err
	}

	if loaded.MaxTurns, err = lookupInt(settings, root, keyMaxTurns, loaded.MaxTurns); err != nil {
		return loaded, err
	}

	if loaded.CompactAt, err = lookupInt(settings, root, keyCompactAt, loaded.CompactAt); err != nil {
		return loaded, err
	}

	return loaded, nil
}

func lookup(settings store.Store, dir, key string) (string, bool, error) {
	value, found, err := settings.Setting(dir, key)

	if err != nil || found {
		return value, found, err
	}

	return settings.Setting(store.GlobalScope, key)
}

func lookupInt(settings store.Store, dir, key string, fallback int) (int, error) {
	value, found, err := lookup(settings, dir, key)

	if err != nil || !found {
		return fallback, err
	}

	number, err := strconv.Atoi(value)

	if err != nil || number < 0 {
		return fallback, fmt.Errorf("setting %s must be a non-negative integer, got %q", key, value)
	}

	return number, nil
}

// programCommand resolves the placeholders of a registered command.
func programCommand(command []string) ([]string, error) {
	resolved := make([]string, len(command))
	copy(resolved, command)

	if len(resolved) > 0 && resolved[0] == selfPlaceholder {
		self, err := os.Executable()

		if err != nil {
			return nil, err
		}

		resolved[0] = self
	}

	return resolved, nil
}
