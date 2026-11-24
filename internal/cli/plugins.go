package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"

	"go.yaml.in/yaml/v4"

	"github.com/LiasCode/rumbo/internal/ambient"
	"github.com/LiasCode/rumbo/internal/extension"
	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/plugins/rumbo"
)

// This file owns the plugins: a plugin is a directory that brings programs,
// providers, front ends, and extensions, and skills, all at once, which its
// manifest names. What it brings is there as what rumbo has built in is,
// without an entry of its own in the configuration file, which keeps of a
// plugin only where it comes from and what the user agreed it replaces.
// rumbo's own plugin (plugins/rumbo) is one more, there before any the user
// adds, whose manifest is built into the executable with its programs.

const (
	// manifestFile is the file at a plugin's root that says what it brings.
	manifestFile = "plugin.yaml"
	// pluginPlaceholder stands, in a command of the manifest, for the
	// directory the plugin is in, wherever it was added from.
	pluginPlaceholder = "{plugin}"
	// skillsDir is where a plugin keeps its skills, each in a directory.
	skillsDir = "skills"
)

// pluginName is what a plugin, and a program it brings, can be called.
var pluginName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// manifest is what a plugin says it brings.
type manifest struct {
	Name        string                     `yaml:"name"`
	Description string                     `yaml:"description"`
	Providers   map[string]manifestProgram `yaml:"providers"`
	Frontends   map[string]manifestProgram `yaml:"frontends"`
	Extensions  map[string]manifestProgram `yaml:"extensions"`
}

// manifestProgram is a program of a plugin: the command that starts it, the
// configuration the plugin gives it, which what the user sets goes over,
// and, for an extension, the tools of rumbo's own it asks to replace.
type manifestProgram struct {
	Command  []string       `yaml:"command"`
	Config   map[string]any `yaml:"config"`
	Replaces []string       `yaml:"replaces"`
}

// programs returns what the manifest brings of a kind.
func (m manifest) programs(kind string) map[string]manifestProgram {
	switch kind {
	case store.KindProvider:
		return m.Providers
	case store.KindFrontend:
		return m.Frontends
	}

	return m.Extensions
}

// plugin is a plugin the user added, or rumbo's own: where it is, and what
// it brings. rumbo's own has no directory.
type plugin struct {
	store.Plugin
	dir      string
	manifest manifest
}

// ownPlugin is rumbo's own plugin, read from the manifest the executable
// was built with. It comes from no file of the user's, so its source is
// that it is built in.
var ownPlugin = sync.OnceValues(func() (plugin, error) {
	said, err := parseManifest(rumbo.Manifest, "rumbo's own "+manifestFile)

	return plugin{Plugin: store.Plugin{Name: said.Name, Source: sourceBuiltin}, manifest: said}, err
})

// isOwn reports whether the plugin called name is rumbo's own.
func isOwn(name string) bool {
	own, err := ownPlugin()

	return err == nil && own.Name == name
}

// pluginKinds are the kinds of programs a plugin brings, in the order they
// are said.
var pluginKinds = []string{store.KindProvider, store.KindFrontend, store.KindExtension}

// remote reports whether a source is the address of a Git repository, and
// not a directory.
func remote(source string) bool {
	return strings.Contains(source, "://") || strings.HasPrefix(source, "git@")
}

// pluginsDir is where the plugins added from an address are kept.
func pluginsDir() (string, error) {
	data, err := DataDir()

	if err != nil {
		return "", err
	}

	return filepath.Join(data, "plugins"), nil
}

// dirOf is where a plugin is: the directory it was added from, or the copy
// of its repository rumbo keeps.
func dirOf(added store.Plugin) (string, error) {
	if !remote(added.Source) {
		return added.Source, nil
	}

	kept, err := pluginsDir()

	if err != nil {
		return "", err
	}

	return filepath.Join(kept, added.Name), nil
}

// readManifest reads what the plugin in dir brings, refusing what rumbo
// would read something else from than its writer meant.
func readManifest(dir string) (manifest, error) {
	path := filepath.Join(dir, manifestFile)
	raw, err := os.ReadFile(path)

	if err != nil {
		return manifest{}, fmt.Errorf("it has no %s, which says what a plugin brings: %w", manifestFile, err)
	}

	return parseManifest(raw, path)
}

// parseManifest reads a manifest, which path names in what is said of it.
func parseManifest(raw []byte, path string) (manifest, error) {
	var read manifest

	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)

	// A key that is not one of the manifest's is said in its own words.
	if err := decoder.Decode(&read); err != nil {
		said := strings.NewReplacer(
			"in type cli.manifestProgram", "among what a program has: command, config, and replaces",
			"in type cli.manifest", "among what a plugin has: name, description, providers, frontends, and extensions",
		).Replace(err.Error())

		return read, fmt.Errorf("%s: %s", path, said)
	}

	if !pluginName.MatchString(read.Name) {
		return read, fmt.Errorf("%s: name is %q, and a plugin's is lowercase letters, digits, _ and -, starting with a letter", path, read.Name)
	}

	for _, kind := range pluginKinds {
		for name, program := range read.programs(kind) {
			switch {
			case !pluginName.MatchString(name):
				return read, fmt.Errorf("%s: the %s %q is not named in lowercase letters, digits, _ and -, starting with a letter", path, kindNames[kind], name)

			case len(program.Command) == 0:
				return read, fmt.Errorf("%s: the %s %s has no command to start it", path, kindNames[kind], name)

			case len(program.Replaces) > 0 && kind != store.KindExtension:
				return read, fmt.Errorf("%s: the %s %s has replaces, which only an extension has", path, kindNames[kind], name)
			}

			if _, built := builtin(kind, name); built {
				return read, fmt.Errorf("%s: rumbo has a %s called %s built in, so a plugin cannot bring one", path, kindNames[kind], name)
			}
		}
	}

	return read, nil
}

// installed returns the plugins there are, rumbo's own first and then the
// ones the user added, by name, each with what it brings. One that cannot
// be read, or that brings what one before it does, fails all of it, saying
// how to take it out: its programs would otherwise be gone without a word.
func installed(database store.Store) ([]plugin, error) {
	own, err := ownPlugin()

	if err != nil {
		return nil, err
	}

	added, err := database.Plugins()

	if err != nil {
		return nil, err
	}

	plugins := []plugin{own}

	for _, each := range added {
		dir, err := dirOf(each)

		if err != nil {
			return nil, err
		}

		said, err := readManifest(dir)

		if err == nil && each.Name == own.Name {
			err = fmt.Errorf("rumbo's own plugin is called %s", own.Name)
		}

		if err == nil {
			err = broughtAlready(plugins, said)
		}

		if err != nil {
			return nil, fmt.Errorf(`the plugin %s, in %s: %w; "rumbo plugin rm %s" takes it out`, each.Name, dir, err, each.Name)
		}

		plugins = append(plugins, plugin{Plugin: each, dir: dir, manifest: said})
	}

	return plugins, nil
}

// broughtAlready refuses a manifest that brings a program one of the
// plugins brings.
func broughtAlready(plugins []plugin, said manifest) error {
	for _, kind := range pluginKinds {
		for _, name := range slices.Sorted(maps.Keys(said.programs(kind))) {
			for _, other := range plugins {
				if _, brought := other.manifest.programs(kind)[name]; brought {
					return fmt.Errorf("it brings the %s %s, which the plugin %s already brings", kindNames[kind], name, other.Name)
				}
			}
		}
	}

	return nil
}

// program returns the program of a kind the plugin brings under name, as
// it is started before what the user set for it: its command in the
// plugin's directory, the configuration the plugin gives it, and the tools
// it replaces among those the user agreed to.
func (p plugin) program(kind, name string) (store.Program, bool) {
	said, found := p.manifest.programs(kind)[name]

	if !found {
		return store.Program{}, false
	}

	brought := store.Program{Name: name, Command: make([]string, len(said.Command))}

	for index, word := range said.Command {
		brought.Command[index] = strings.ReplaceAll(word, pluginPlaceholder, p.dir)
	}

	if len(said.Config) > 0 {
		brought.Config, _ = json.Marshal(said.Config)
	}

	for _, tool := range said.Replaces {
		if slices.Contains(p.Replaces, tool) {
			brought.Replaces = append(brought.Replaces, tool)
		}
	}

	return brought, true
}

// broughtBy returns the program of a kind a plugin brings under name, and
// which plugin.
func broughtBy(database store.Store, kind, name string) (store.Program, string, error) {
	plugins, err := installed(database)

	if err != nil {
		return store.Program{}, "", err
	}

	for _, each := range plugins {
		if brought, found := each.program(kind, name); found {
			return brought, each.Name, nil
		}
	}

	return store.Program{}, "", nil
}

// skillPlaces are where the plugins keep their skills. rumbo's own, which
// has no directory, has none.
func skillPlaces(database store.Store) ([]string, error) {
	plugins, err := installed(database)

	if err != nil {
		return nil, err
	}

	var places []string

	for _, each := range plugins {
		if each.dir != "" {
			places = append(places, filepath.Join(each.dir, skillsDir))
		}
	}

	return places, nil
}

// addPlugin adds the plugin at source, a directory or the address of a Git
// repository, which is copied. Everything it brings is started first, with
// what there already is, which is the test that it works; a plugin that
// does not pass is not added. agreed are the tools of rumbo's own the user
// lets its extensions replace, which must be all it asks for.
func addPlugin(database store.Store, source string, agreed []string, diagnostics io.Writer) (string, error) {
	dir, copied, err := fetch(source)

	if err != nil {
		return "", err
	}

	// A copy that is not added in the end is not left behind.
	added := false

	defer func() {
		if copied && !added {
			_ = os.RemoveAll(dir)
		}
	}()

	said, err := readManifest(dir)

	if err != nil {
		return "", fmt.Errorf("%s: %w", source, err)
	}

	if copied {
		if dir, err = settle(dir, said.Name); err != nil {
			return "", err
		}
	} else {
		source = dir
	}

	if err := fits(database, said, agreed, false); err != nil {
		return "", fmt.Errorf("the plugin %s was not added: %w", said.Name, err)
	}

	candidate := store.Plugin{Name: said.Name, Source: source, Replaces: agreed}

	if err := database.PutPlugin(candidate); err != nil {
		return "", err
	}

	if err := tryPlugin(database, plugin{Plugin: candidate, dir: dir, manifest: said}, diagnostics); err != nil {
		_ = database.DeletePlugin(said.Name)

		return "", fmt.Errorf("the plugin %s was not added: %w", said.Name, err)
	}

	added = true

	return "added the plugin " + said.Name + ", which brings:\n" + brings(plugin{Plugin: candidate, dir: dir, manifest: said}), nil
}

// fetch returns the directory of the plugin at source: the directory
// itself, or a copy of the repository at an address, in a place of its own
// until its name is known.
func fetch(source string) (dir string, copied bool, err error) {
	if !remote(source) {
		dir, err = filepath.Abs(source)

		return dir, false, err
	}

	kept, err := pluginsDir()

	if err != nil {
		return "", false, err
	}

	if err := os.MkdirAll(kept, 0o755); err != nil {
		return "", false, err
	}

	if dir, err = os.MkdirTemp(kept, ".adding-*"); err != nil {
		return "", false, err
	}

	if output, err := exec.Command("git", "clone", "--quiet", "--depth", "1", "--", source, dir).CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir)

		return "", false, fmt.Errorf("%s could not be copied with git: %s", source, strings.TrimSpace(string(output)))
	}

	return dir, true, nil
}

// settle moves a copy to where the plugin called name is kept.
func settle(dir, name string) (string, error) {
	kept := filepath.Join(filepath.Dir(dir), name)

	if _, err := os.Stat(kept); err == nil {
		return dir, fmt.Errorf(`there is a plugin called %s already; "rumbo plugin update %s" brings it up to date, and "rumbo plugin rm %s" takes it out`, name, name, name)
	}

	return kept, os.Rename(dir, kept)
}

// fits refuses a plugin that cannot be beside what there is: one of its
// name, unless it is that one again, as it is now; a program another plugin
// brings or the user registered; or a replacement the user did not agree
// to.
func fits(database store.Store, said manifest, agreed []string, again bool) error {
	plugins, err := installed(database)

	if err != nil {
		return err
	}

	// One of its name is the plugin itself when it is updated, and is said
	// below when it is added.
	others := slices.DeleteFunc(slices.Clone(plugins), func(other plugin) bool { return other.Name == said.Name })

	if err := broughtAlready(others, said); err != nil {
		return err
	}

	var asked []string

	for _, kind := range pluginKinds {
		registered, err := database.Programs(kind)

		if err != nil {
			return err
		}

		for _, name := range slices.Sorted(maps.Keys(said.programs(kind))) {
			if slices.ContainsFunc(registered, func(mine store.Program) bool { return mine.Name == name && len(mine.Command) > 0 }) {
				return fmt.Errorf(`it brings the %[1]s %[2]s, and you registered one of that name; "rumbo %[3]s rm %[2]s" takes yours out`, kindNames[kind], name, kind)
			}

			asked = append(asked, said.programs(kind)[name].Replaces...)
		}
	}

	if isOwn(said.Name) {
		return fmt.Errorf("rumbo's own plugin is called %s; give yours another name in its %s", said.Name, manifestFile)
	}

	for _, plugin := range plugins {
		if plugin.Name == said.Name && !again {
			return fmt.Errorf(`there is a plugin of that name already, from %s; "rumbo plugin rm %s" takes it out`, plugin.Source, said.Name)
		}
	}

	slices.Sort(asked)
	asked = slices.Compact(asked)

	// What it replaces is the user's to say, never the plugin's alone.
	for _, tool := range asked {
		if slices.Contains(agreed, tool) {
			continue
		}

		how := "add it"

		if again {
			how = "take it out and add it again"
		}

		return fmt.Errorf("it has an extension take the place of rumbo's own %[1]s; %[2]s with -replaces %[1]s to agree", strings.Join(asked, ","), how)
	}

	for _, tool := range agreed {
		if !slices.Contains(asked, tool) {
			return fmt.Errorf("-replaces names %s, which nothing of it asks to replace", tool)
		}
	}

	return nil
}

// tryPlugin starts what a plugin that is added brings, with what there
// already is, and stops it: its extensions with all the others, and each of
// its providers. A front end needs the terminal, so it is tried when it is
// first used.
func tryPlugin(database store.Store, added plugin, diagnostics io.Writer) error {
	for _, name := range slices.Sorted(maps.Keys(added.manifest.Providers)) {
		program, err := findProgram(database, store.KindProvider, name)

		if err != nil {
			return err
		}

		if err := tryStart(program, diagnostics); err != nil {
			return fmt.Errorf("its provider %s did not start: %w", name, err)
		}
	}

	if len(added.manifest.Extensions) == 0 {
		return nil
	}

	programs, err := extensionPrograms(database)

	if err != nil {
		return err
	}

	set := extension.NewSet(func(string) io.Writer { return diagnostics }, func(text string) string { return text })
	defer set.Close()

	_, err = set.Start(context.Background(), programs, reserved())

	return err
}

// brings says what a plugin brings, a line to each thing.
func brings(added plugin) string {
	var out strings.Builder

	for _, kind := range pluginKinds {
		for _, name := range slices.Sorted(maps.Keys(added.manifest.programs(kind))) {
			brought, _ := added.program(kind, name)

			fmt.Fprintf(&out, "  the %s %s", kindNames[kind], name)

			if len(brought.Replaces) > 0 {
				fmt.Fprintf(&out, ", in place of rumbo's own %s", strings.Join(brought.Replaces, ", "))
			}

			out.WriteString("\n")
		}
	}

	// Its skills are those in its own directory, among all that are found.
	// rumbo's own has no directory, and none.
	if added.dir != "" {
		place := filepath.Join(added.dir, skillsDir)

		for _, skill := range ambient.FindSkills(context.Background(), added.dir, []string{place}).Found {
			if strings.HasPrefix(skill.Path, place+string(filepath.Separator)) {
				fmt.Fprintf(&out, "  the skill %s\n", skill.Name)
			}
		}
	}

	if out.Len() == 0 {
		return "  nothing"
	}

	return strings.TrimRight(out.String(), "\n")
}

// removePlugin takes a plugin out: what the configuration file keeps of
// it, what the user set for the programs it brought, which are gone with
// it, and the copy rumbo kept of its repository.
func removePlugin(database store.Store, name string) (string, error) {
	added, err := database.Plugins()

	if err != nil {
		return "", err
	}

	index := slices.IndexFunc(added, func(each store.Plugin) bool { return each.Name == name })

	switch {
	case index < 0 && isOwn(name):
		return "", fmt.Errorf(`the plugin %s comes with rumbo and is always there; "rumbo provider rm <name>" and "rumbo frontend rm <name>" take out what you set for what it brings`, name)

	case index < 0:
		return "", fmt.Errorf(`there is no plugin called %s; "rumbo plugin list" says which there are`, name)
	}

	dir, err := dirOf(added[index])

	if err != nil {
		return "", err
	}

	said := "removed the plugin " + name

	// A manifest that can no longer be read names nothing to forget.
	if brought, err := readManifest(dir); err == nil {
		for _, kind := range pluginKinds {
			for program := range brought.programs(kind) {
				chosen, registered, err := chosenProgram(database, kind, program)

				if err != nil {
					return "", err
				}

				if registered && len(chosen.Command) == 0 {
					if err := database.DeleteProgram(kind, program); err != nil {
						return "", err
					}

					said += fmt.Sprintf("\nand what you had set for its %s %s", kindNames[kind], program)
				}
			}
		}
	}

	if err := database.DeletePlugin(name); err != nil {
		return "", err
	}

	if remote(added[index].Source) {
		if err := os.RemoveAll(dir); err != nil {
			return "", err
		}
	}

	return said, nil
}

// updatePlugin brings the copy of a plugin's repository up to date, and
// tries what it brings now. A plugin that no longer passes goes back to
// what it was.
func updatePlugin(database store.Store, name string, diagnostics io.Writer) (string, error) {
	plugins, err := installed(database)

	if err != nil {
		return "", err
	}

	index := slices.IndexFunc(plugins, func(each plugin) bool { return each.Name == name })

	switch {
	case index < 0:
		return "", fmt.Errorf(`there is no plugin called %s; "rumbo plugin list" says which there are`, name)

	case isOwn(name):
		return "", fmt.Errorf("the plugin %s comes with rumbo, and is as new as the rumbo you have", name)

	case !remote(plugins[index].Source):
		return "", fmt.Errorf("the plugin %s is the directory %s, read as it is each time: there is nothing to update", name, plugins[index].dir)
	}

	current := plugins[index]
	git := func(args ...string) (string, error) {
		output, err := exec.Command("git", append([]string{"-C", current.dir}, args...)...).CombinedOutput()

		return strings.TrimSpace(string(output)), err
	}

	before, err := git("rev-parse", "HEAD")

	if err != nil {
		return "", fmt.Errorf("the copy of %s in %s is not a Git repository any more: %s", name, current.dir, before)
	}

	if output, err := git("pull", "--quiet", "--ff-only"); err != nil {
		return "", fmt.Errorf("%s could not be brought up to date with git: %s", name, output)
	}

	if after, _ := git("rev-parse", "HEAD"); after == before {
		return "the plugin " + name + " is up to date", nil
	}

	back := func(why error) (string, error) {
		_, _ = git("reset", "--quiet", "--hard", before)

		return "", fmt.Errorf("the plugin %s was left as it was, since what it is now does not pass: %w", name, why)
	}

	said, err := readManifest(current.dir)

	if err != nil {
		return back(err)
	}

	if said.Name != name {
		return back(fmt.Errorf("it is called %s now", said.Name))
	}

	// What it asks to replace now is checked against what was agreed.
	if err := fits(database, said, current.Replaces, true); err != nil {
		return back(err)
	}

	if err := tryPlugin(database, plugin{Plugin: current.Plugin, dir: current.dir, manifest: said}, diagnostics); err != nil {
		return back(err)
	}

	return "updated the plugin " + name + ", which brings:\n" + brings(plugin{Plugin: current.Plugin, dir: current.dir, manifest: said}), nil
}

const pluginUsage = `usage:
  rumbo plugin list                               rumbo's own plugin, and those you added
  rumbo plugin show <name>                        where it is, and what it brings
  rumbo plugin add [-replaces <tools>] <source>   a directory, or the address of a Git repository
  rumbo plugin update <name>                      bring one added from an address up to date
  rumbo plugin rm <name>                          take one out, with what you set for what it brought

A plugin is a directory with a plugin.yaml that names the programs it brings,
providers, front ends, and extensions, and with skills in skills/. What it
brings is there once it is added, as what rumbo has built in is, and is set
with "rumbo provider set", "rumbo frontend set", and "rumbo extension set".
rumbo's own plugin, rumbo, is built into it and always there; "rumbo plugin
show rumbo" says what it brings.
A plugin runs with your rights: add one whose code you trust. When one of its
extensions takes the place of a tool of rumbo's own, such as bash, adding it
says so and asks you to agree with -replaces.`

func pluginCommands(database store.Store, args []string) error {
	if len(args) == 0 {
		return errors.New(pluginUsage)
	}

	switch {
	case args[0] == "list" && len(args) == 1:
		plugins, err := installed(database)

		if err != nil {
			return err
		}

		out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

		for _, each := range plugins {
			fmt.Fprintf(out, "%s\t%s\t%s\n", each.Name, each.Source, strings.Join(strings.Fields(each.manifest.Description), " "))
		}

		return out.Flush()

	case args[0] == "show" && len(args) == 2:
		plugins, err := installed(database)

		if err != nil {
			return err
		}

		index := slices.IndexFunc(plugins, func(each plugin) bool { return each.Name == args[1] })

		if index < 0 {
			return fmt.Errorf(`there is no plugin called %s; "rumbo plugin list" says which there are`, args[1])
		}

		shown := plugins[index]
		where := "in " + shown.dir

		// One added from an address is kept in a copy, and rumbo's own is
		// in rumbo.
		switch {
		case isOwn(shown.Name):
			where = "built into rumbo"

		case remote(shown.Source):
			where = "from " + shown.Source + ", kept in " + shown.dir
		}

		fmt.Printf("%s: %s\n%s\nit brings:\n%s\n", shown.Name, strings.Join(strings.Fields(shown.manifest.Description), " "), where, brings(shown))

		return nil

	case args[0] == "add":
		flags := flag.NewFlagSet("plugin add", flag.ContinueOnError)
		replaces := flags.String(settingReplaces, "", "")

		if err := flags.Parse(args[1:]); err != nil {
			return err
		}

		if flags.NArg() != 1 {
			return errors.New(pluginUsage)
		}

		added, err := addPlugin(database, flags.Arg(0), replacing(*replaces), os.Stderr)

		if err != nil {
			return err
		}

		fmt.Println(added)

		return nil

	case args[0] == "update" && len(args) == 2:
		updated, err := updatePlugin(database, args[1], os.Stderr)

		if err != nil {
			return err
		}

		fmt.Println(updated)

		return nil

	case args[0] == "rm" && len(args) == 2:
		removed, err := removePlugin(database, args[1])

		if err != nil {
			return err
		}

		fmt.Println(removed)

		return nil
	}

	return errors.New(pluginUsage)
}
