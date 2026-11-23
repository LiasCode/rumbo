package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/LiasCode/rumbo/internal/provider"
	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/protocol"
)

// This file owns what a person is shown of the configuration: all of it at
// once, as it applies, each value with where it comes from, without starting
// anything; and one provider in full, which it starts to know its defaults.
// The command line and the host's functions both go through it, so both
// show the same.

// sessionLayer is what a session chose for itself, which wins over the rest
// in it.
type sessionLayer struct {
	title  string
	choice choice
}

// describeConfig says what is chosen and what applies, level by level, each
// value with where it comes from. A session, when there is one, comes first,
// since what it chose wins. later says how to see a program's defaults,
// which only the program knows.
func describeConfig(database store.Store, session *sessionLayer, later string) (string, error) {
	var out strings.Builder

	if chosenIn, err := ConfigPath(); err == nil {
		fmt.Fprintf(&out, "chosen in %s\n", chosenIn)
	}

	if data, err := DataDir(); err == nil {
		fmt.Fprintf(&out, "sessions in %s\n", filepath.Join(data, "sessions"))
	}

	table := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)

	if session != nil {
		model := session.choice.model

		if model == "" {
			model = "(not set)"
		}

		fmt.Fprintf(table, "\n%s\n", session.title)
		fmt.Fprintf(table, "  provider\t%s\t%s\n", session.choice.provider, sourceSession)
		fmt.Fprintf(table, "  model\t%s\t%s\n", model, sourceSession)

		options := objectFields(session.choice.options)

		for _, name := range slices.Sorted(maps.Keys(options)) {
			fmt.Fprintf(table, "  %s\t%s\t%s\n", name, shownValue(options[name]), sourceSession)
		}
	}

	settings, err := effectiveSettings(database)

	if err != nil {
		return "", err
	}

	fmt.Fprintf(table, "\nsystem\n")
	writeSettings(table, settings, "  ")

	for _, kind := range []string{store.KindProvider, store.KindFrontend, store.KindExtension} {
		if err := describePrograms(table, database, kind); err != nil {
			return "", err
		}
	}

	plugins, err := database.Plugins()

	if err != nil {
		return "", err
	}

	// There are plugins only when the user added some.
	if len(plugins) > 0 {
		fmt.Fprintf(table, "\nplugins\n")
	}

	for _, plugin := range plugins {
		fmt.Fprintf(table, "  %s\t%s\t%s\n", plugin.Name, plugin.Source, sourceFile)

		if len(plugin.Replaces) > 0 {
			fmt.Fprintf(table, "    %s\t%s\t%s\n", settingReplaces, strings.Join(plugin.Replaces, ", "), sourceFile)
		}
	}

	if err := table.Flush(); err != nil {
		return "", err
	}

	fmt.Fprintf(&out, "\nWhat is not listed is the program's own default; %s says those.", later)

	return out.String(), nil
}

// writeSettings writes the system settings as they apply, each with where
// its value comes from, or what applies when it is not set.
func writeSettings(table io.Writer, settings []effective, indent string) {
	for index, setting := range settings {
		value, source := setting.Value, setting.Source

		if source == sourceUnset {
			value, source = "(not set)", settingSpecs[index].unset
		}

		fmt.Fprintf(table, "%s%s\t%s\t%s\n", indent, setting.Key, value, source)
	}
}

// describePrograms writes the programs of a kind there are, each with what
// was chosen for it and how rumbo has it built in.
func describePrograms(table io.Writer, database store.Store, kind string) error {
	programs, err := listPrograms(database, kind)

	if err != nil {
		return err
	}

	// There are extensions only when the user registered some.
	if len(programs) == 0 {
		return nil
	}

	fmt.Fprintf(table, "\n%ss\n", kindNames[kind])

	for _, program := range programs {
		chosen, _, err := chosenProgram(database, kind, program.Name)

		if err != nil {
			return err
		}

		base, origin, err := provided(database, kind, program.Name)

		if err != nil {
			return err
		}

		started := origin

		if len(chosen.Command) > 0 {
			started = strings.Join(chosen.Command, " ")

			if origin == sourceBuiltin {
				started += ", in place of the built-in one"
			}
		}

		// Where the program comes from goes where its values' sources go.
		fmt.Fprintf(table, "  %s\t\t%s\n", program.Name, started)

		if chosen.Model != "" {
			fmt.Fprintf(table, "    %s\t%s\t%s\n", settingModel, chosen.Model, sourceFile)
		}

		switch {
		case len(chosen.Replaces) > 0:
			fmt.Fprintf(table, "    %s\t%s\t%s\n", settingReplaces, strings.Join(chosen.Replaces, ", "), sourceFile)

		// What a plugin's extension replaces is what the user agreed to
		// when they added the plugin.
		case len(base.Replaces) > 0 && len(chosen.Command) == 0:
			fmt.Fprintf(table, "    %s\t%s\t%s\n", settingReplaces, strings.Join(base.Replaces, ", "), origin)
		}

		comes, mine := objectFields(nil), objectFields(chosen.Config)

		if len(chosen.Command) == 0 {
			comes = objectFields(base.Config)
		}

		for _, name := range slices.Sorted(maps.Keys(mergedKeys(comes, mine))) {
			value, source := mine[name], sourceFile

			if _, set := mine[name]; !set {
				value, source = comes[name], origin
			}

			fmt.Fprintf(table, "    %s\t%s\t%s\n", name, shownValue(value), source)
		}
	}

	return nil
}

// mergedKeys are the names in any of the objects given.
func mergedKeys(objects ...map[string]json.RawMessage) map[string]bool {
	names := map[string]bool{}

	for _, object := range objects {
		for name := range object {
			names[name] = true
		}
	}

	return names
}

// showProvider starts a provider and says, for each of its settings, the
// value that applies and where it comes from: the session, when the provider
// is the one it uses; the configuration file; how rumbo has it built in; or
// the provider's own default. Then it says what else the provider offers.
// It is also the test that the provider starts.
func showProvider(database store.Store, name string, session *choice, diagnostics io.Writer) (string, error) {
	chosen, registered, err := chosenProgram(database, store.KindProvider, name)

	if err != nil {
		return "", err
	}

	program, err := startedWith(database, store.KindProvider, name, chosen, registered)

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

	offers := process.Description()

	if session != nil && session.provider != name {
		session = nil
	}

	layers := settingLayers{chosen: objectFields(chosen.Config), inForce: objectFields(process.Config())}
	layers.comes, layers.origin = providedFields(database, store.KindProvider, chosen)

	var out strings.Builder

	started := strings.Join(program.Command, " ")

	if layers.origin == sourceBuiltin {
		started += ", built in"
	} else if layers.origin != "" {
		started += ", from the " + layers.origin
	}

	fmt.Fprintf(&out, "%s: %s", name, started)

	model, source := "(not set)", ""

	switch {
	case session != nil:
		model, source = session.model, sourceSession
	case chosen.Model != "":
		model, source = chosen.Model, sourceFile
	}

	writeSetting(&out, settingModel, model, source, "the model; the one set for "+name+" is what a new session that uses it starts with", nil)

	shownNames := map[string]bool{settingModel: true}

	for _, field := range offers.Fields {
		value, source := layers.of(field)
		writeSetting(&out, field.Name, value, source, field.Description, field.Enum)
		shownNames[field.Name] = true
	}

	for _, option := range offers.Options {
		shownNames[option.Name] = true
	}

	// What the provider does not describe is shown as it is, and one only
	// the file holds is one the provider says nothing of, which keeps the
	// provider from working when it says what its configuration holds.
	known := &settingsOf{offers: offers, inForce: layers.inForce}

	for _, other := range slices.Sorted(maps.Keys(mergedKeys(layers.chosen, layers.comes, layers.inForce))) {
		if shownNames[other] {
			continue
		}

		value, source := layers.of(protocol.Option{Name: other})
		about := ""

		if _, read := layers.inForce[other]; !read {
			about = name + " says nothing of it, and may not read it"

			if known.tells() {
				about = fmt.Sprintf(`%s has no such setting, and does not work until "rumbo provider set %s %s=" takes it out`, name, name, other)
			}
		}

		writeSetting(&out, other, value, source, about, nil)
	}

	sessionChose := map[string]json.RawMessage{}

	if session != nil {
		sessionChose = objectFields(session.options)
	}

	for _, option := range offers.Options {
		value, source := layers.of(option)

		if chose, found := sessionChose[option.Name]; found {
			value, source = shownValue(chose), sourceSession
		}

		about := strings.TrimSpace("an option, which a session can choose for itself. " + option.Description)
		writeSetting(&out, option.Name, value, source, about, option.Enum)
	}

	if len(offers.Functions) > 0 {
		out.WriteString("\nfunctions:")
	}

	for _, function := range offers.Functions {
		fmt.Fprintf(&out, "\n    %s: %s", function.Name, function.Description)
	}

	return out.String(), nil
}

// settingLayers are where a provider's setting can come from, under a
// session's choice: the configuration file, how the provider comes, built
// into rumbo or brought by a plugin, which origin says, and the provider's
// configuration in force, which holds its defaults.
type settingLayers struct {
	chosen, comes, inForce map[string]json.RawMessage
	origin                 string
}

// of returns the value of a setting, and where it comes from.
func (layers settingLayers) of(described protocol.Option) (string, string) {
	if value, found := layers.chosen[described.Name]; found {
		return shownValue(value), sourceFile
	}

	if value, found := layers.comes[described.Name]; found {
		return shownValue(value), layers.origin
	}

	if value, found := layers.inForce[described.Name]; found {
		return shownValue(value), sourceDefault
	}

	if len(described.Default) > 0 {
		return shownValue(described.Default), sourceDefault
	}

	return "(not set)", ""
}

// writeSetting writes one setting of a provider: its value, where it comes
// from, and, under them, what it is and the values it takes.
func writeSetting(out *strings.Builder, name, value, source, about string, values []string) {
	fmt.Fprintf(out, "\n%s = %s", name, value)

	if source != "" {
		fmt.Fprintf(out, "  (%s)", source)
	}

	if about != "" {
		fmt.Fprintf(out, "\n    %s", about)
	}

	if len(values) > 0 {
		fmt.Fprintf(out, "\n    values: %s", strings.Join(values, ", "))
	}
}
