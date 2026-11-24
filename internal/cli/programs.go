package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/LiasCode/rumbo/internal/provider"
	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/protocol"
)

// This file owns the programs rumbo starts, providers, front ends, and
// extensions: the ones it has built in, the ones plugins bring, and the ones
// the user registers, adding one, changing its configuration, and asking a
// provider what it offers. The command line and the host's functions both go
// through it.
//
// A program's configuration holds what the user chose for it, and nothing
// else. What is not chosen is the program's own default, which only the
// program knows, and says when it starts.

// selfPlaceholder in a registered command stands for the rumbo executable, so
// the programs it serves itself, its own plugin's among them, keep working
// wherever the binary is installed.
const selfPlaceholder = "{self}"

// builtins are the programs served by the rumbo executable itself, by kind,
// with the subcommand that serves them, apart from those of its own plugin
// (plugins.go): plain, the front end for pipes and scripts, which needs no
// plugin. They are there without being registered.
var builtins = []struct {
	kind       string
	subcommand string
	names      []string
}{
	{store.KindFrontend, "attach", []string{"plain"}},
}

// kindNames are what a person calls each kind of program.
var kindNames = map[string]string{
	store.KindProvider:  "provider",
	store.KindFrontend:  "front end",
	store.KindExtension: "extension",
}

// builtin returns the program of a kind that rumbo has built in under name.
func builtin(kind, name string) (store.Program, bool) {
	for _, group := range builtins {
		if group.kind == kind && slices.Contains(group.names, name) {
			return store.Program{Name: name, Command: []string{selfPlaceholder, group.subcommand, name}}, true
		}
	}

	return store.Program{}, false
}

// provided returns the program of a kind that is there under name without
// the user registering it, and where it comes from: one rumbo has built in,
// or one a plugin brings. The origin is empty when there is none.
func provided(database store.Store, kind, name string) (store.Program, string, error) {
	if base, built := builtin(kind, name); built {
		return base, sourceBuiltin, nil
	}

	brought, plugin, err := broughtBy(database, kind, name)

	if err != nil || plugin == "" {
		return store.Program{}, "", err
	}

	return brought, sourcePlugin + plugin, nil
}

// sourcePlugin starts where a program a plugin brings comes from, before
// the plugin's name.
const sourcePlugin = "plugin "

// findProgram returns the program called name as it is started: the one the
// user registered, or the one rumbo has built in or a plugin brings, with
// what the user chose for it over what it comes with.
func findProgram(database store.Store, kind, name string) (store.Program, error) {
	chosen, registered, err := chosenProgram(database, kind, name)

	if err != nil {
		return store.Program{}, err
	}

	return startedWith(database, kind, name, chosen, registered)
}

// chosenProgram returns what the user chose for a program, and whether they
// chose anything: a built-in nothing was chosen for is in no file.
func chosenProgram(database store.Store, kind, name string) (store.Program, bool, error) {
	chosen, err := database.Program(kind, name)

	if errors.Is(err, store.ErrNotFound) {
		return store.Program{Name: name}, false, nil
	}

	return chosen, err == nil, err
}

// startedWith is a program as it is started, given what the user chose for
// it.
func startedWith(database store.Store, kind, name string, chosen store.Program, registered bool) (store.Program, error) {
	base, origin, err := provided(database, kind, name)

	if err != nil {
		return store.Program{}, err
	}

	built := origin != ""

	switch {
	case !registered && !built:
		return store.Program{}, fmt.Errorf(`%s %q is not registered; see "rumbo %s list"`, kindNames[kind], name, kind)

	// One registered under the name of a built-in, or of one a plugin
	// brings, takes its place.
	case len(chosen.Command) > 0:
		return chosen, nil

	case !built:
		return store.Program{}, fmt.Errorf("%s %q has no command in %s", kindNames[kind], name, configFile)
	}

	config, err := mergeOptions(base.Config, chosen.Config)

	if err != nil {
		return store.Program{}, fmt.Errorf("the configuration of %s: %w", name, err)
	}

	base.Config, base.Model = config, chosen.Model

	// What the user has it replace goes over what a plugin does.
	if len(chosen.Replaces) > 0 {
		base.Replaces = chosen.Replaces
	}

	return base, nil
}

// providedFields are the fields of a program's configuration as rumbo has
// it built in, or as a plugin brings it, and which of the two; nothing for
// a program registered in its place.
func providedFields(database store.Store, kind string, chosen store.Program) (map[string]json.RawMessage, string) {
	base, origin, err := provided(database, kind, chosen.Name)

	if err != nil || origin == "" || len(chosen.Command) > 0 {
		return nil, ""
	}

	return objectFields(base.Config), origin
}

// objectFields reads a JSON object by its fields; anything else has none.
func objectFields(object json.RawMessage) map[string]json.RawMessage {
	fields := map[string]json.RawMessage{}

	if len(object) > 0 {
		_ = json.Unmarshal(object, &fields)
	}

	return fields
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

	plugins, err := installed(database)

	if err != nil {
		return nil, err
	}

	for _, each := range plugins {
		names = append(names, slices.Collect(maps.Keys(each.manifest.programs(kind)))...)
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

// settingModel is the model a new session starts with when it uses a
// provider. It is set as a provider's fields are, but kept beside its
// configuration: the host reads it, not the provider.
const settingModel = "model"

// setProgram changes what is chosen for a program: for a provider, its
// model, a field of its configuration, or one of its options; for a front
// end or an extension, a field of its configuration, and for an extension
// the tools of rumbo's it replaces too. Each change is
// name=value, where an empty value gives the setting back to its default. A
// provider is started first, for what it says of its settings, and then with
// the changes, which is the test that it takes them: when it does not,
// nothing is stored. An extension is started with the changes too. A front
// end needs the terminal to start, so what it is given is checked when it
// next starts. It returns a line saying each change.
func setProgram(database store.Store, kind, name string, written []string, diagnostics io.Writer) ([]string, error) {
	changes, err := parseChanges(written)

	if err != nil {
		return nil, err
	}

	chosen, registered, err := chosenProgram(database, kind, name)

	if err != nil {
		return nil, err
	}

	current, err := startedWith(database, kind, name, chosen, registered)

	if err != nil {
		return nil, err
	}

	// A provider that does not start as it is takes the changes as they are
	// written, which may be what makes it start.
	var known *settingsOf

	if kind == store.KindProvider {
		known, _ = learnSettings(current, diagnostics)
	}

	fields := objectFields(chosen.Config)
	configured := false
	said := make([]string, len(changes))

	for index, each := range changes {
		if each.name == settingModel && kind == store.KindProvider {
			if each.back() && chosen.Model == "" {
				return nil, fmt.Errorf("%s has no model set in %s, so there is nothing to give back", name, configFile)
			}

			chosen.Model = each.value
			said[index] = settingModel + " = " + each.value + "; a new session that uses " + name + " starts with it"

			if each.back() {
				said[index] = settingModel + ": taken out; a new session that uses " + name + " has to be given one"
			}

			continue
		}

		if each.name == settingReplaces && kind == store.KindExtension {
			if each.back() && len(chosen.Replaces) == 0 {
				return nil, fmt.Errorf("%s replaces nothing in %s, so there is nothing to give back", name, configFile)
			}

			chosen.Replaces = replacing(each.value)
			said[index] = settingReplaces + " = " + strings.Join(chosen.Replaces, ", ") + "; they are in place of rumbo's own"

			if each.back() {
				said[index] = settingReplaces + ": taken out; rumbo's own tools are the model's again"
			}

			continue
		}

		configured = true

		if each.back() {
			if _, found := fields[each.name]; !found {
				return nil, nothingToGiveBack(database, kind, chosen, each.name)
			}

			delete(fields, each.name)
			said[index] = each.name + ": given back to its default"

			continue
		}

		described, err := known.setting(name, each.name)

		if err != nil {
			return nil, err
		}

		value, err := encodeValue(described, each.value)

		if err != nil {
			return nil, err
		}

		fields[each.name] = value
		said[index] = each.name + " = " + shownValue(value)
	}

	chosen.Name, chosen.Config = name, nil

	if len(fields) > 0 {
		chosen.Config, _ = json.Marshal(fields)
	}

	if configured && kind == store.KindProvider {
		next, err := startedWith(database, kind, name, chosen, true)

		if err != nil {
			return nil, err
		}

		if err := tryStart(next, diagnostics); err != nil {
			return nil, fmt.Errorf("%s did not start with that, so nothing was changed: %w", name, err)
		}
	}

	// An extension is tried with any change, since what it replaces is
	// checked against what it declares.
	if kind == store.KindExtension {
		next, err := startedWith(database, kind, name, chosen, true)

		if err != nil {
			return nil, err
		}

		if _, err := tryExtension(database, next, diagnostics); err != nil {
			return nil, fmt.Errorf("%s did not start with that, so nothing was changed: %w", name, err)
		}
	}

	// One that is there anyway, with nothing chosen for it, has nothing to
	// keep.
	if _, origin, _ := provided(database, kind, name); origin != "" && len(chosen.Command) == 0 && chosen.Model == "" && len(chosen.Config) == 0 && len(chosen.Replaces) == 0 {
		if err := database.DeleteProgram(kind, name); err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}

		return said, nil
	}

	return said, database.PutProgram(kind, chosen)
}

// nothingToGiveBack says why a field that is not set for a program cannot
// be given back.
func nothingToGiveBack(database store.Store, kind string, chosen store.Program, name string) error {
	text := fmt.Sprintf("%s is not set for %s in %s, so there is nothing to give back", name, chosen.Name, configFile)
	fields, origin := providedFields(database, kind, chosen)

	value, found := fields[name]

	switch {
	case found && origin == sourceBuiltin:
		text += fmt.Sprintf("; %s is how rumbo has it built in, and setting it changes that", shownValue(value))

	case found:
		text += fmt.Sprintf("; %s is how the %s brings it, and setting it changes that", shownValue(value), origin)
	}

	return errors.New(text)
}

// settingsOf is what a provider says of its settings when it starts: the
// fields of its configuration and its options, as it describes them, and
// its configuration in force, with a default for every field.
type settingsOf struct {
	offers  protocol.Description
	inForce map[string]json.RawMessage
}

// learnSettings starts a provider for what it says of its settings.
func learnSettings(program store.Program, diagnostics io.Writer) (*settingsOf, error) {
	command, err := programCommand(program.Command)

	if err != nil {
		return nil, err
	}

	process, err := provider.Start(context.Background(), command, program.Config, diagnostics)

	if err != nil {
		return nil, err
	}

	defer process.Close()

	return &settingsOf{offers: process.Description(), inForce: objectFields(process.Config())}, nil
}

// setting returns what describes the setting called name: a field or an
// option the provider describes, or else a field its configuration in force
// holds, by the value it has. Without anything known of the provider, any
// name is taken, as it is written.
func (known *settingsOf) setting(provider, name string) (protocol.Option, error) {
	if known == nil {
		return protocol.Option{Name: name}, nil
	}

	for _, described := range [][]protocol.Option{known.offers.Fields, known.offers.Options} {
		if index := slices.IndexFunc(described, func(option protocol.Option) bool { return option.Name == name }); index >= 0 {
			return described[index], nil
		}
	}

	if value, found := known.inForce[name]; found {
		return typeOf(name, value), nil
	}

	return protocol.Option{}, fmt.Errorf("%s has no setting %q; its settings are %s", provider, name, strings.Join(known.names(), ", "))
}

// names are the settings a provider has: its model, the fields of its
// configuration, and its options.
func (known *settingsOf) names() []string {
	names := []string{settingModel}

	for _, described := range [][]protocol.Option{known.offers.Fields, known.offers.Options} {
		for _, option := range described {
			names = append(names, option.Name)
		}
	}

	var others []string

	for name := range known.inForce {
		if !slices.Contains(names, name) {
			others = append(others, name)
		}
	}

	slices.Sort(others)

	return append(names, others...)
}

// tells reports whether a provider says what its configuration holds: it
// describes fields of it, or says what is in force. One that says neither
// gives nothing to check a field against.
func (known *settingsOf) tells() bool {
	return len(known.offers.Fields) > 0 || len(known.inForce) > 0
}

// checkChosen refuses what the user chose for a provider when it holds a
// field the provider has no setting of, by the rule a change is checked by,
// so one written in the file by hand, such as a misspelled one, is not
// ignored. A provider that says nothing of its configuration is taken as it
// is.
func checkChosen(database store.Store, name string, known *settingsOf) error {
	if !known.tells() {
		return nil
	}

	chosen, _, err := chosenProgram(database, store.KindProvider, name)

	if err != nil {
		return err
	}

	for _, field := range slices.Sorted(maps.Keys(objectFields(chosen.Config))) {
		if _, err := known.setting(name, field); err != nil {
			return fmt.Errorf(`%s, under providers, %s, config: %w; "rumbo provider set %s %s=" takes it out`, configFile, name, err, name, field)
		}
	}

	return nil
}

// tryStart starts a provider and stops it, which is the test that it starts.
func tryStart(program store.Program, diagnostics io.Writer) error {
	command, err := programCommand(program.Command)

	if err != nil {
		return err
	}

	process, err := provider.Start(context.Background(), command, program.Config, diagnostics)

	if err != nil {
		return err
	}

	return process.Close()
}

// removeProgram takes out what the user registered under name, and says what
// that leaves. A built-in is there all the same, as rumbo has it.
func removeProgram(database store.Store, kind, name string) (string, error) {
	_, origin, err := provided(database, kind, name)

	if err != nil {
		return "", err
	}

	plugin, brought := strings.CutPrefix(origin, sourcePlugin)
	err = database.DeleteProgram(kind, name)

	switch {
	case errors.Is(err, store.ErrNotFound) && brought && isOwn(plugin):
		return "", fmt.Errorf("%s %q comes with rumbo's own plugin, with nothing of yours to remove; it is always there", kindNames[kind], name)

	case errors.Is(err, store.ErrNotFound) && brought:
		return "", fmt.Errorf(`%s %q comes with the plugin %s, with nothing of yours to remove; "rumbo plugin rm %s" takes the plugin out`, kindNames[kind], name, plugin, plugin)

	case errors.Is(err, store.ErrNotFound) && origin != "":
		return "", fmt.Errorf("%s %q is built in, with nothing of yours to remove; it is always there", kindNames[kind], name)

	case errors.Is(err, store.ErrNotFound):
		return "", fmt.Errorf("%s %q is not registered", kindNames[kind], name)

	case err != nil:
		return "", err

	case brought:
		return fmt.Sprintf("removed what was yours of the %s %s, which is as the plugin %s brings it again", kindNames[kind], name, plugin), nil

	case origin != "":
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

// shown is a JSON object as a person reads it, an empty one when nothing is
// in it.
func shown(object json.RawMessage) string {
	if len(object) == 0 {
		return "{}"
	}

	return string(object)
}
