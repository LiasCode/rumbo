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
// one, changing its configuration, and asking a provider what it offers. The
// command line and the host's functions both go through it.

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
}{
	{store.KindProvider, "provide", []string{"anthropic", "openai"}},
	{store.KindFrontend, "attach", []string{"tui", "plain"}},
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
				program := store.Program{Name: name, Command: []string{selfPlaceholder, builtin.subcommand, name}}

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
	if name == "" || strings.ContainsAny(name, "/ ") {
		return errors.New(`a name is one word without "/"`)
	}

	if len(command) == 0 {
		return errors.New("a command is needed to start it")
	}

	if _, err := mergeOptions(config); err != nil {
		return fmt.Errorf("its configuration: %w", err)
	}

	return database.PutProgram(kind, store.Program{Name: name, Command: command, Config: config})
}

// configureProgram changes fields of a program's configuration and leaves
// the rest. Each change is name=value; a value that is valid JSON is kept as
// it is, anything else is text, and an empty one removes the field.
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

	for _, change := range changes {
		field, value, found := strings.Cut(change, "=")

		if !found || field == "" {
			return program, fmt.Errorf("%q is not name=value", change)
		}

		if value == "" {
			delete(fields, field)
			continue
		}

		if !json.Valid([]byte(value)) {
			quoted, _ := json.Marshal(value)
			value = string(quoted)
		}

		fields[field] = json.RawMessage(value)
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

	return describeProgram(program) + "\n" + describeOffers(process.Description()), nil
}

func describeProgram(program store.Program) string {
	return fmt.Sprintf("name: %s\ncommand: %s\nconfiguration: %s", program.Name, strings.Join(program.Command, " "), program.Config)
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
