package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LiasCode/rumbo/internal/store"
)

// pluginIn writes a plugin in a directory of its own: its manifest, and a
// skill called after it.
func pluginIn(t *testing.T, manifest string) string {
	t.Helper()

	dir := t.TempDir()
	skill := filepath.Join(dir, skillsDir, "how")

	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}

	write := func(path, text string) {
		t.Helper()

		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(filepath.Join(dir, manifestFile), manifest)
	write(filepath.Join(skill, "SKILL.md"), "---\nname: how\ndescription: How the plugin's team works.\n---\n")

	return dir
}

// finder is a plugin whose extension has one tool, find, started by the
// test binary, with a configuration of its own.
const finder = `name: finder
description: Finds things.
extensions:
  finder:
    command: ["{self}", extend, find]
    config:
      depth: 2
`

// A plugin brings its programs and its skills at once: they are there as
// what rumbo has built in is, with what the user sets over what the plugin
// gives, and only where the plugin comes from is kept.
func TestAPluginBringsItsProgramsAndSkills(t *testing.T) {
	h := testHost(t)
	dir := pluginIn(t, finder)

	added, err := addPlugin(h.database, dir, nil, io.Discard)

	if err != nil || added != "added the plugin finder, which brings:\n  the extension finder\n  the skill how" {
		t.Fatalf("added = %q, err = %v", added, err)
	}

	kept, _ := h.database.Plugins()

	if len(kept) != 1 || kept[0].Source != dir {
		t.Fatalf("kept %+v", kept)
	}

	if registered, _ := h.database.Programs(store.KindExtension); len(registered) != 0 {
		t.Fatalf("the plugin's extension was written as the user's: %+v", registered)
	}

	// What the user sets goes over what the plugin gives, and is all that
	// is kept of the program.
	if _, err := setProgram(h.database, store.KindExtension, "finder", []string{"told=true"}, io.Discard); err != nil {
		t.Fatal(err)
	}

	program, err := findProgram(h.database, store.KindExtension, "finder")

	if err != nil || program.Command[0] != selfPlaceholder || string(program.Config) != `{"depth":2,"told":true}` {
		t.Fatalf("program = %+v, err = %v", program, err)
	}

	worker, err := h.agent(context.Background(), nil, h.current.choice, nil, h.current.tools, nil)

	if err != nil || worker.Tools[len(worker.Tools)-1].Def().Name != "find" {
		t.Fatalf("worker = %+v, err = %v", worker, err)
	}

	told := worker.Collectors[2].Collect(context.Background(), t.TempDir())

	if !strings.Contains(told, "how: How the plugin's team works.") || worker.Collectors[3].Name() != "where" {
		t.Fatalf("the skills section:\n%s", told)
	}

	if shown, err := describeConfig(h.database, nil, ""); err != nil || !inOrder(shown, "extensions", "finder", "plugin finder", "depth", "plugin finder", "told", "config.yaml", "plugins", "finder", dir) {
		t.Fatalf("config = %q, err = %v", shown, err)
	}

	// What is the user's of it can be removed, and it not.
	if removed, err := removeProgram(h.database, store.KindExtension, "finder"); err != nil || removed != "removed what was yours of the extension finder, which is as the plugin finder brings it again" {
		t.Fatalf("removed = %q, err = %v", removed, err)
	}

	if _, err := removeProgram(h.database, store.KindExtension, "finder"); err == nil || !strings.Contains(err.Error(), `"rumbo plugin rm finder" takes the plugin out`) {
		t.Fatalf("err = %v", err)
	}

	// Taking the plugin out takes what the user set for what it brought.
	if _, err := setProgram(h.database, store.KindExtension, "finder", []string{"told=true"}, io.Discard); err != nil {
		t.Fatal(err)
	}

	removed, err := removePlugin(h.database, "finder")

	if err != nil || removed != "removed the plugin finder\nand what you had set for its extension finder" {
		t.Fatalf("removed = %q, err = %v", removed, err)
	}

	if _, err := findProgram(h.database, store.KindExtension, "finder"); err == nil {
		t.Fatal("the extension is still there")
	}

	if _, err := os.Stat(filepath.Join(dir, manifestFile)); err != nil {
		t.Fatalf("a plugin added from a directory lost its files: %v", err)
	}
}

// inOrder reports whether text has the parts, each after the one before.
func inOrder(text string, parts ...string) bool {
	for _, part := range parts {
		index := strings.Index(text, part)

		if index < 0 {
			return false
		}

		text = text[index+len(part):]
	}

	return true
}

func TestAPluginThatDoesNotFitIsNotAdded(t *testing.T) {
	h := testHost(t)

	if _, err := addPlugin(h.database, pluginIn(t, finder), nil, io.Discard); err != nil {
		t.Fatal(err)
	}

	if _, err := registerExtension(h.database, "mine", extendWith("mine"), nil, nil, io.Discard); err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct{ manifest, want string }{
		"its name is taken":          {finder, "there is a plugin of that name already"},
		"another brings the program": {"name: other\nextensions:\n  finder:\n    command: [x]\n", "it brings the extension finder, which the plugin finder already brings"},
		"the user registered one":    {"name: other\nextensions:\n  mine:\n    command: [x]\n", `it brings the extension mine, and you registered one of that name; "rumbo extension rm mine" takes yours out`},
		"rumbo has one built in":     {"name: other\nproviders:\n  openai:\n    command: [x]\n", "rumbo has a provider called openai built in"},
		"a misspelled key":           {"name: other\nextension:\n  a:\n    command: [x]\n", "field extension not found among what a plugin has: name, description, providers, frontends, and extensions"},
		"a program without command":  {"name: other\nextensions:\n  a:\n    config: {a: 1}\n", "the extension a has no command to start it"},
		"a provider that replaces":   {"name: other\nproviders:\n  a:\n    command: [x]\n    replaces: [bash]\n", "has replaces, which only an extension has"},
		"no name":                    {"description: nameless\n", `name is ""`},
		"an extension that fails":    {"name: other\nextensions:\n  broken:\n    command: [/nonexistent/extension]\n", "the plugin other was not added: extension broken: start extension"},
		"a tool another has":         {"name: other\nextensions:\n  second:\n    command: [\"{self}\", extend, find]\n", `extension second declares the tool "find", which the extension finder already has`},
	}

	for name, each := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := addPlugin(h.database, pluginIn(t, each.manifest), nil, io.Discard)

			if err == nil || !strings.Contains(err.Error(), each.want) {
				t.Fatalf("err = %v, want %q in it", err, each.want)
			}

			if kept, _ := h.database.Plugins(); len(kept) != 1 {
				t.Fatalf("it was kept: %+v", kept)
			}
		})
	}
}

// An extension of a plugin takes the place of a tool of rumbo's only when
// the user agreed, by name, as they added the plugin.
func TestAPluginReplacesAToolOnlyWhenTheUserAgreed(t *testing.T) {
	h := testHost(t)
	dir := pluginIn(t, "name: sandbox\nextensions:\n  sandbox:\n    command: [\"{self}\", extend, bash]\n    replaces: [bash]\n")

	if _, err := addPlugin(h.database, dir, nil, io.Discard); err == nil || !strings.Contains(err.Error(), "add it with -replaces bash to agree") {
		t.Fatalf("without agreeing: %v", err)
	}

	if _, err := addPlugin(h.database, dir, []string{"bash", "read"}, io.Discard); err == nil || !strings.Contains(err.Error(), "-replaces names read, which nothing of it asks to replace") {
		t.Fatalf("agreeing to more: %v", err)
	}

	added, err := addPlugin(h.database, dir, []string{"bash"}, io.Discard)

	if err != nil || !strings.Contains(added, "the extension sandbox, in place of rumbo's own bash") {
		t.Fatalf("added = %q, err = %v", added, err)
	}

	worker, err := h.agent(context.Background(), nil, h.current.choice, nil, h.current.tools, nil)

	if err != nil || len(worker.Tools) != 4 {
		t.Fatalf("worker has %d tools, err = %v", len(worker.Tools), err)
	}

	result, err := worker.Tools[3].Run(context.Background(), "/repo", json.RawMessage(`{}`))

	if err != nil || result.Text != "called in /repo" {
		t.Fatalf("bash = %q, err = %v", result.Text, err)
	}
}

// A plugin added from an address is a copy of its repository, which update
// brings up to date, unless what it is now does not pass; taking the
// plugin out takes the copy.
func TestAPluginFromARepositoryIsCopiedAndUpdated(t *testing.T) {
	t.Setenv("RUMBO_DATA", t.TempDir())

	h := testHost(t)
	repository := pluginIn(t, finder)

	git := func(args ...string) {
		t.Helper()

		command := exec.Command("git", append([]string{"-C", repository, "-c", "user.email=test@example.com", "-c", "user.name=test"}, args...)...)

		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, output)
		}
	}

	commit := func(manifest string) {
		t.Helper()

		if err := os.WriteFile(filepath.Join(repository, manifestFile), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}

		git("add", "-A")
		git("commit", "--quiet", "-m", "a change")
	}

	git("init", "--quiet")
	commit(finder)

	source := "file://" + repository

	if _, err := addPlugin(h.database, source, nil, io.Discard); err != nil {
		t.Fatal(err)
	}

	kept, _ := pluginsDir()
	program, err := findProgram(h.database, store.KindExtension, "finder")

	if err != nil || string(program.Config) != `{"depth":2}` {
		t.Fatalf("program = %+v, err = %v", program, err)
	}

	if _, err := os.Stat(filepath.Join(kept, "finder", manifestFile)); err != nil {
		t.Fatalf("the copy: %v", err)
	}

	if said, err := updatePlugin(h.database, "finder", io.Discard); err != nil || said != "the plugin finder is up to date" {
		t.Fatalf("said = %q, err = %v", said, err)
	}

	commit(strings.Replace(finder, "depth: 2", "depth: 3", 1))

	if said, err := updatePlugin(h.database, "finder", io.Discard); err != nil || !strings.HasPrefix(said, "updated the plugin finder") {
		t.Fatalf("said = %q, err = %v", said, err)
	}

	// What does not pass leaves the plugin as it was.
	commit(strings.Replace(finder, `["{self}", extend, find]`, `[/nonexistent/extension]`, 1))

	if _, err := updatePlugin(h.database, "finder", io.Discard); err == nil || !strings.Contains(err.Error(), "the plugin finder was left as it was") {
		t.Fatalf("err = %v", err)
	}

	if program, err := findProgram(h.database, store.KindExtension, "finder"); err != nil || string(program.Config) != `{"depth":3}` || program.Command[0] != selfPlaceholder {
		t.Fatalf("after an update that did not pass: %+v, err = %v", program, err)
	}

	if _, err := removePlugin(h.database, "finder"); err != nil {
		t.Fatal(err)
	}

	if entries, _ := os.ReadDir(kept); len(entries) != 0 {
		t.Fatalf("the copy was left behind: %v", entries)
	}
}
