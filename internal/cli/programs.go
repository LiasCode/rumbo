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

// This file owns the programs rumbo starts, providers and front ends: the
// ones it has built in and the ones the user registers, adding one, changing
// its configuration, and asking a provider what it offers. The command line
// and the host's functions both go through it.
//
// A program's configuration holds what the user chose for it, and nothing
// else. What is not chosen is the program's own default, which only the
// program knows, and says when it starts.

// selfPlaceholder in a registered command stands for the rumbo executable, so
// built-in programs keep working wherever the binary is installed.
const selfPlaceholder = "{self}"

// builtins are the programs served by the rumbo executable itself, by kind,
// with the subcommand that serves them. They are there without being
// registered.
var builtins = []struct {
	kind       string
	subcommand string
	names      []string
	// configs are what a built-in starts with, by name, when it is more
	// than its defaults. What the user chooses for it goes over them.
	configs map[string]json.RawMessage
}{
	{store.KindProvider, "provide", []string{"anthropic", "openai", "chatgpt"}, map[string]json.RawMessage{
		// The provider speaks Chat Completions unless told otherwise, as most
		// servers do; OpenAI's own reasoning models need the Responses API to
		// use tools with an effort.
		"openai": json.RawMessage(`{"api":"responses"}`),
	}},
	{store.KindFrontend, "attach", []string{"tui", "plain"}, nil},
}

// kindNames are what a person calls each kind of program.
var kindNames = map[string]string{
	store.KindProvider: "provider",
	store.KindFrontend: "front end",
}

// builtin returns the program of a kind that rumbo has built in under name.
func builtin(kind, name string) (store.Program, bool) {
	for _, group := range builtins {
		if group.kind == kind && slices.Contains(group.names, name) {
			return store.Program{Name: name, Command: []string{selfPlaceholder, group.subcommand, name}, Config: group.configs[name]}, true
		}
	}

	return store.Program{}, false
}

// findProgram returns the program called name as it is started: the one the
// user registered, or the one rumbo has built in, with what the user chose
// for it over what it is built in with.
func findProgram(database store.Store, kind, name string) (store.Program, error) {
	chosen, err := database.Program(kind, name)

	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Program{}, err
	}

	registered := err == nil
	base, built := builtin(kind, name)

	switch {
	case !registered && !built:
		return store.Program{}, fmt.Errorf(`%s %q is not registered; see "rumbo %s list"`, kindNames[kind], name, kind)

	case !registered:
		return base, nil

	// One registered under the name of a built-in takes its place.
	case len(chosen.Command) > 0:
		return chosen, nil

	case !built:
		return store.Program{}, fmt.Errorf("%s %q has no command in %s", kindNames[kind], name, configFile)
	}

	if base.Config, err = mergeOptions(base.Config, chosen.Config); err != nil {
		return store.Program{}, fmt.Errorf("the configuration of %s: %w", name, err)
	}

	return base, nil
}

// listPrograms returns the programs of a kind there are, built in and
// registered, by name, each as it is started.
func listPrograms(database store.Store, kind string) ([]store.Program, error) {
	registered, err := database.Programs(kind)

	if err != nil {
		return nil, err
	}

	var names []string

	for _, group := range builtins {
		if group.kind == kind {
			names = append(names, group.names...)
		}
	}

	for _, program := range registered {
		if !slices.Contains(names, program.Name) {
			names = append(names, program.Name)
		}
	}

	slices.Sort(names)

	programs := make([]store.Program, len(names))

	for index, name := range names {
		if programs[index], err = findProgram(database, kind, name); err != nil {
			return nil, err
		}
	}

	return programs, nil
}

// registerProgram adds a program, or replaces the one with that name.
func registerProgram(database store.Store, kind, name string, command []string, config json.RawMessage) error {
	if err := checkProgram(name, command, config); err != nil {
		return err
	}

	return database.PutProgram(kind, store.Program{Name: name, Command: command, Config: config})
}

// registerProvider registers a provider after starting it, which is the
// test that it works. One that does not start is not registered.
func registerProvider(database store.Store, name string, command []string, config json.RawMessage, diagnostics io.Writer) error {
	if err := checkProgram(name, command, config); err != nil {
		return err
	}

	argv, err := programCommand(command)

	if err != nil {
		return err
	}

	process, err := provider.Start(context.Background(), argv, config, diagnostics)

	if err != nil {
		return fmt.Errorf("provider %q was not registered: %w", name, err)
	}

	defer process.Close()

	return database.PutProgram(store.KindProvider, store.Program{Name: name, Command: command, Config: config})
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

// configureProgram changes fields of what the user chose for a program and
// leaves the rest. Each change is name=value, read as applyChanges does; an
// empty value takes the field out, so the program's default applies. It
// returns the program as it is started from then on.
func configureProgram(database store.Store, kind, name string, changes []string) (store.Program, error) {
	if _, err := findProgram(database, kind, name); err != nil {
		return store.Program{}, err
	}

	// A built-in nothing was chosen for yet is registered nowhere.
	chosen, err := database.Program(kind, name)

	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Program{}, err
	}

	chosen.Name = name

	if chosen.Config, err = changeObject(chosen.Config, changes); err != nil {
		return store.Program{}, err
	}

	// A built-in with nothing chosen for it has nothing to keep.
	if len(chosen.Command) == 0 && len(chosen.Config) == 0 {
		err = database.DeleteProgram(kind, name)

		if errors.Is(err, store.ErrNotFound) {
			err = nil
		}
	} else {
		err = database.PutProgram(kind, chosen)
	}

	if err != nil {
		return store.Program{}, err
	}

	return findProgram(database, kind, name)
}

// removeProgram takes out what the user registered under name, and says what
// that leaves. A built-in is there all the same, as rumbo has it.
func removeProgram(database store.Store, kind, name string) (string, error) {
	_, built := builtin(kind, name)
	err := database.DeleteProgram(kind, name)

	switch {
	case errors.Is(err, store.ErrNotFound) && built:
		return "", fmt.Errorf("%s %q is built in, with nothing of yours to remove; it is always there", kindNames[kind], name)

	case errors.Is(err, store.ErrNotFound):
		return "", fmt.Errorf("%s %q is not registered", kindNames[kind], name)

	case err != nil:
		return "", err

	case built:
		return fmt.Sprintf("removed what was yours of the %s %s, which is built in and is as rumbo has it again", kindNames[kind], name), nil
	}

	return fmt.Sprintf("removed the %s %s", kindNames[kind], name), nil
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
	program, err := findProgram(database, store.KindProvider, name)

	if err != nil {
		return "", err
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

	described := describeProgram(program)

	// Only the program knows the defaults of what was not chosen for it.
	if inForce := process.Config(); len(inForce) > 0 {
		described += fmt.Sprintf("\nin force, with its defaults: %s", inForce)
	}

	return described + "\n" + describeOffers(process.Description()), nil
}

// describeProgram says how a program is started, and what was chosen for it.
func describeProgram(program store.Program) string {
	return fmt.Sprintf("name: %s\ncommand: %s\nconfiguration: %s", program.Name, strings.Join(program.Command, " "), shown(program.Config))
}

// shown is a JSON object as a person reads it, an empty one when nothing is
// in it.
func shown(object json.RawMessage) string {
	if len(object) == 0 {
		return "{}"
	}

	return string(object)
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
