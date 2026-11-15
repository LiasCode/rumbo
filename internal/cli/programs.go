package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/LiasCode/rumbo/internal/provider"
	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/protocol"
)

// This file owns the registered programs, providers and front ends: adding
// one, changing its configuration, asking a provider what it offers, and
// keeping the defaults a program gives. The command line and the host's
// functions both go through it.
//
// Every field of a program's configuration, and every option of a provider,
// has a value in its registration: the one the user gave, or else the
// program's own default, which the host stores the first time the program
// says it.

// selfPlaceholder in a registered command stands for the rumbo executable, so
// built-in programs keep working wherever the binary is installed.
const selfPlaceholder = "{self}"

// keySeeded holds the built-in programs already registered once, so one the
// user removes stays removed.
const keySeeded = internalPrefix + "builtins"

// builtins are the programs served by the rumbo executable itself, by kind,
// with the subcommand that serves them.
var builtins = []struct {
	kind       string
	subcommand string
	names      []string
	// configs are what a built-in is registered with, by name, when it is
	// more than its defaults.
	configs map[string]json.RawMessage
}{
	{store.KindProvider, "provide", []string{"anthropic", "openai"}, map[string]json.RawMessage{
		// The provider speaks Chat Completions unless told otherwise, as most
		// servers do; OpenAI's own reasoning models need the Responses API to
		// use tools with an effort.
		"openai": json.RawMessage(`{"api":"responses"}`),
	}},
	{store.KindFrontend, "attach", []string{"tui", "plain"}, nil},
}

// seedBuiltins registers each built-in program the first time this database
// meets it.
func seedBuiltins(database store.Store) error {
	var seeded []string

	if value, found, err := database.Setting(store.GlobalScope, keySeeded); err != nil {
		return err
	} else if found {
		_ = json.Unmarshal([]byte(value), &seeded)
	}

	changed := false

	for _, builtin := range builtins {
		for _, name := range builtin.names {
			id := builtin.kind + "/" + name

			if slices.Contains(seeded, id) {
				continue
			}

			// One that is already there was put by an earlier version, or by
			// the user, and is left as it is.
			if _, err := database.Program(builtin.kind, name); errors.Is(err, store.ErrNotFound) {
				program := store.Program{Name: name, Command: []string{selfPlaceholder, builtin.subcommand, name}, Config: builtin.configs[name]}

				if err := database.PutProgram(builtin.kind, program); err != nil {
					return err
				}
			}

			seeded = append(seeded, id)
			changed = true
		}
	}

	if !changed {
		return nil
	}

	raw, _ := json.Marshal(seeded)

	return database.SetSetting(store.GlobalScope, keySeeded, string(raw))
}

// registerProgram adds a program, or replaces the one with that name.
func registerProgram(database store.Store, kind, name string, command []string, config json.RawMessage) error {
	if err := checkProgram(name, command, config); err != nil {
		return err
	}

	return database.PutProgram(kind, store.Program{Name: name, Command: command, Config: config})
}

// registerProvider registers a provider after starting it, which is also
// the test that it works, and stores the defaults it gives. One that does
// not start is not registered. It returns the options the provider gives no
// default for.
func registerProvider(database store.Store, name string, command []string, config json.RawMessage, diagnostics io.Writer) ([]string, error) {
	if err := checkProgram(name, command, config); err != nil {
		return nil, err
	}

	argv, err := programCommand(command)

	if err != nil {
		return nil, err
	}

	process, err := provider.Start(context.Background(), argv, config, diagnostics)

	if err != nil {
		return nil, fmt.Errorf("provider %q was not registered: %w", name, err)
	}

	defer process.Close()

	if err := database.PutProgram(store.KindProvider, store.Program{Name: name, Command: command, Config: config}); err != nil {
		return nil, err
	}

	return adopt(database, store.KindProvider, name, process.Config(), process.Description())
}

func checkProgram(name string, command []string, config json.RawMessage) error {
	if name == "" || strings.ContainsAny(name, "/ ") {
		return errors.New(`a name is one word without "/"`)
	}

	if len(command) == 0 {
		return errors.New("a command is needed to start it")
	}

	if _, err := mergeOptions(config); err != nil {
		return fmt.Errorf("its configuration: %w", err)
	}

	return nil
}

// adopt stores what a program said about itself, for what its registration
// does not hold yet: the default of each field of its configuration, and of
// each option a provider describes. What is stored stays. It returns the
// options described without a default, which the provider decides itself.
func adopt(database store.Store, kind, name string, config json.RawMessage, described protocol.Description) ([]string, error) {
	program, err := database.Program(kind, name)

	if err != nil {
		return nil, err
	}

	stored, err := mergeOptions(program.Config)

	if err != nil {
		return nil, fmt.Errorf("the configuration of %s: %w", name, err)
	}

	merged, err := mergeOptions(config, stored)

	if err != nil {
		return nil, fmt.Errorf("the configuration %s says is in force: %w", name, err)
	}

	defaults := map[string]json.RawMessage{}
	var undecided []string

	for _, option := range described.Options {
		if len(option.Default) == 0 {
			undecided = append(undecided, option.Name)
			continue
		}

		defaults[option.Name] = option.Default
	}

	chosen, err := mergeOptions(program.Options)

	if err != nil {
		return nil, fmt.Errorf("the options of %s: %w", name, err)
	}

	options := chosen

	if len(defaults) > 0 {
		raw, _ := json.Marshal(defaults)

		if options, err = mergeOptions(raw, chosen); err != nil {
			return nil, err
		}
	}

	if string(merged) == string(stored) && string(options) == string(chosen) {
		return undecided, nil
	}

	program.Config, program.Options = merged, options

	return undecided, database.PutProgram(kind, program)
}

// configureProgram changes fields of a program's configuration and leaves
// the rest. Each change is name=value, read as applyChanges does.
func configureProgram(database store.Store, kind, name string, changes []string) (store.Program, error) {
	program, err := database.Program(kind, name)

	if err != nil {
		return program, fmt.Errorf("%s %q is not registered", kind, name)
	}

	fields := map[string]json.RawMessage{}

	if len(program.Config) > 0 {
		if err := json.Unmarshal(program.Config, &fields); err != nil {
			return program, fmt.Errorf("its stored configuration is not an object: %w", err)
		}
	}

	if err := applyChanges(fields, changes); err != nil {
		return program, err
	}

	program.Config, _ = json.Marshal(fields)

	return program, database.PutProgram(kind, program)
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

// inspectProvider starts a registered provider, asks what it offers, and
// stops it. It is also the test that the program speaks the protocol.
func inspectProvider(database store.Store, name string, diagnostics io.Writer) (string, error) {
	program, err := database.Program(store.KindProvider, name)

	if err != nil {
		return "", fmt.Errorf("provider %q is not registered", name)
	}

	command, err := programCommand(program.Command)

	if err != nil {
		return "", err
	}

	process, err := provider.Start(context.Background(), command, program.Config, diagnostics)

	if err != nil {
		return "", err
	}

	defer process.Close()

	if _, err := adopt(database, store.KindProvider, name, process.Config(), process.Description()); err != nil {
		return "", err
	}

	if program, err = database.Program(store.KindProvider, name); err != nil {
		return "", err
	}

	return describeProgram(program) + "\n" + describeOffers(process.Description()), nil
}

func describeProgram(program store.Program) string {
	described := fmt.Sprintf("name: %s\ncommand: %s\nconfiguration: %s", program.Name, strings.Join(program.Command, " "), program.Config)

	if len(program.Options) > 0 {
		described += fmt.Sprintf("\noptions: %s", program.Options)
	}

	return described
}

// undecidedNote says which options a provider gives no default for, if any.
func undecidedNote(name string, undecided []string) string {
	if len(undecided) == 0 {
		return ""
	}

	return fmt.Sprintf("; %s gives no default for %s, so it decides them itself", name, strings.Join(undecided, ", "))
}

// describeOffers renders what a provider says about itself.
func describeOffers(offers protocol.Description) string {
	var out strings.Builder

	out.WriteString("it speaks the protocol")

	if offers.Model != nil {
		fmt.Fprintf(&out, "\nmodel: %s", describeOption(*offers.Model))
	}

	if len(offers.Options) == 0 {
		out.WriteString("\noptions: none described")
	}

	for _, option := range offers.Options {
		fmt.Fprintf(&out, "\noption %s: %s", option.Name, describeOption(option))
	}

	if len(offers.Functions) == 0 {
		out.WriteString("\nfunctions: none described")
	}

	for _, function := range offers.Functions {
		fmt.Fprintf(&out, "\nfunction %s: %s", function.Name, function.Description)
	}

	return out.String()
}

func describeOption(option protocol.Option) string {
	parts := []string{option.Type}

	if len(option.Enum) > 0 {
		parts = append(parts, "one of "+strings.Join(option.Enum, ", "))
	}

	if option.Values != "" {
		parts = append(parts, "values from the function "+option.Values)
	}

	if len(option.Default) > 0 {
		parts = append(parts, "default "+string(option.Default))
	}

	if option.Description != "" {
		parts = append(parts, option.Description)
	}

	return strings.Join(parts, "; ")
}
