package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/LiasCode/rumbo/internal/ambient"
	"github.com/LiasCode/rumbo/internal/extension"
	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/internal/tool"
	"github.com/LiasCode/rumbo/protocol"
)

// This file owns the extensions: which the user registered, as they are
// started, and the test that they give the model what it can be given,
// which adding one, changing one, and a run all go through.

// settingReplaces is the tools of rumbo's own an extension takes the place
// of. It is set as a field of its configuration is, but kept beside it: the
// host reads it, not the extension.
const settingReplaces = "replaces"

// extensionName is what an extension can be called.
var extensionName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// reserved are the names of rumbo's own tools and sections. No extension
// can declare one of them, but for a tool the user has it replace. The
// computer tool's is among them whether it is on or not, so turning it on
// does not break an extension.
func reserved() extension.Reserved {
	var names extension.Reserved

	tools := tool.NewSet()

	for _, each := range append(tools.Tools(), tools.Computer()) {
		names.Tools = append(names.Tools, each.Def().Name)
	}

	for _, each := range ambient.Defaults() {
		names.Sections = append(names.Sections, each.Name())
	}

	return names
}

// extensionPrograms returns the extensions there are, the user's own and
// those their plugins bring, as they are started.
func extensionPrograms(database store.Store) ([]extension.Program, error) {
	registered, err := listPrograms(database, store.KindExtension)

	if err != nil {
		return nil, err
	}

	programs := make([]extension.Program, len(registered))

	for index, each := range registered {
		if programs[index], err = extensionProgram(each); err != nil {
			return nil, err
		}
	}

	return programs, nil
}

// extensionProgram is a registered extension as it is started.
func extensionProgram(registered store.Program) (extension.Program, error) {
	if len(registered.Command) == 0 {
		return extension.Program{}, fmt.Errorf("extension %q has no command in %s", registered.Name, configFile)
	}

	command, err := programCommand(registered.Command)

	if err != nil {
		return extension.Program{}, err
	}

	return extension.Program{Name: registered.Name, Command: command, Config: registered.Config, Replaces: registered.Replaces}, nil
}

// declared is what an extension said it gives when it started. Its
// functions are named as a front end offers them, after the extension.
type declared struct {
	tools     []protocol.ToolDef
	sections  []string
	functions []string
}

// tryExtension starts the registered extensions, with candidate among them
// in place of the one of its name, and stops them: the test that candidate
// starts and that what it declares can be given to the model with what the
// rest declare. It returns what candidate declared.
func tryExtension(database store.Store, candidate store.Program, diagnostics io.Writer) (declared, error) {
	programs, err := extensionPrograms(database)

	if err != nil {
		return declared{}, err
	}

	tried, err := extensionProgram(candidate)

	if err != nil {
		return declared{}, err
	}

	programs = slices.DeleteFunc(programs, func(each extension.Program) bool { return each.Name == tried.Name })
	programs = append(programs, tried)

	set := extension.NewSet(func(string) io.Writer { return diagnostics }, func(text string) string { return text })
	defer set.Close()

	if _, err := set.Start(context.Background(), programs, reserved()); err != nil {
		return declared{}, err
	}

	var said declared

	said.tools, said.sections = set.Declared(tried.Name)

	for _, offer := range set.Functions() {
		if offer.Name != tried.Name {
			continue
		}

		for _, function := range offer.Functions {
			said.functions = append(said.functions, "/"+tried.Name+"-"+function.Name)
		}
	}

	return said, nil
}

// registerExtension registers an extension after starting it with the
// others, which is the test that it works, and says what it gives the
// model. One that does not pass is not registered.
func registerExtension(database store.Store, name string, command []string, config json.RawMessage, replaces []string, diagnostics io.Writer) (string, error) {
	if err := checkProgram(name, command, config); err != nil {
		return "", err
	}

	// A front end names an extension's commands after it.
	if !extensionName.MatchString(name) {
		return "", fmt.Errorf("an extension's name is lowercase letters, digits, _ and -, starting with a letter, not %q", name)
	}

	// An empty configuration is none, as it is once it is kept.
	config, _ = mergeOptions(config)

	candidate := store.Program{Name: name, Command: command, Config: config, Replaces: replaces}
	said, err := tryExtension(database, candidate, diagnostics)

	if err != nil {
		return "", fmt.Errorf("extension %q was not registered: %w", name, err)
	}

	if err := database.PutProgram(store.KindExtension, candidate); err != nil {
		return "", err
	}

	return fmt.Sprintf("added the extension %s: %s", name, said.gives(replaces)), nil
}

// gives says what an extension gives: its tools, those of rumbo's they
// take the place of, its sections, and its commands.
func (said declared) gives(replaces []string) string {
	var parts []string

	if len(said.tools) > 0 {
		names := make([]string, len(said.tools))

		for index, def := range said.tools {
			names[index] = def.Name
		}

		slices.Sort(names)
		parts = append(parts, "the runs that start from now on have its tools: "+strings.Join(names, ", "))
	}

	if len(replaces) > 0 {
		parts = append(parts, "its "+strings.Join(replaces, ", ")+" is in place of rumbo's")
	}

	if len(said.sections) > 0 {
		parts = append(parts, "the model is told its sections: "+strings.Join(slices.Sorted(slices.Values(said.sections)), ", "))
	}

	if len(said.functions) > 0 {
		parts = append(parts, "a front end offers its commands: "+strings.Join(said.functions, ", "))
	}

	if len(parts) == 0 {
		return "it declares nothing, so it gives nothing"
	}

	return strings.Join(parts, "; ")
}

// replacing reads the tools an extension replaces as a person writes them,
// separated by commas.
func replacing(written string) []string {
	var names []string

	for name := range strings.SplitSeq(written, ",") {
		if name = strings.TrimSpace(name); name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}

	return names
}

// showExtension starts an extension with the others and says how it is
// started, what is set for it, and what it gives the model, with what
// declaring its tools adds to every request. It is also the test that it
// works.
func showExtension(database store.Store, name string, diagnostics io.Writer) (string, error) {
	program, err := findProgram(database, store.KindExtension, name)

	if err != nil {
		return "", err
	}

	said, err := tryExtension(database, program, diagnostics)

	if err != nil {
		return "", err
	}

	var out strings.Builder

	fmt.Fprintf(&out, "%s: %s\nconfiguration: %s\n", program.Name, strings.Join(program.Command, " "), shown(program.Config))

	if len(program.Replaces) > 0 {
		fmt.Fprintf(&out, "replaces rumbo's own: %s\n", strings.Join(program.Replaces, ", "))
	}

	if len(said.sections) > 0 {
		fmt.Fprintf(&out, "sections, told to the model when a run starts: %s\n", strings.Join(slices.Sorted(slices.Values(said.sections)), ", "))
	}

	if len(said.functions) > 0 {
		fmt.Fprintf(&out, "commands, in a front end: %s\n", strings.Join(said.functions, ", "))
	}

	if len(said.tools) == 0 {
		out.WriteString("it declares no tools")

		return out.String(), nil
	}

	tools := slices.Clone(said.tools)
	slices.SortFunc(tools, func(a, b protocol.ToolDef) int { return strings.Compare(a.Name, b.Name) })

	raw, _ := json.Marshal(tools)

	fmt.Fprintf(&out, "tools, declared to the model in every request (%d bytes):\n", len(raw))

	table := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)

	for _, def := range tools {
		about, _, _ := strings.Cut(strings.TrimSpace(def.Description), "\n")
		fmt.Fprintf(table, "  %s\t%s\n", def.Name, about)
	}

	_ = table.Flush()

	return strings.TrimRight(out.String(), "\n"), nil
}
