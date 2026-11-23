package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// written is the configuration file as it is on disk.
func written(t *testing.T, files *Files) string {
	t.Helper()

	raw, err := os.ReadFile(files.config)

	if err != nil {
		t.Fatal(err)
	}

	return string(raw)
}

// The file holds what was set, written as a person would write it, and
// nothing while nothing is set.
func TestSettingsAreWrittenAsAPersonWould(t *testing.T) {
	files := testFiles(t)

	if _, found, err := files.Setting("max_turns"); err != nil || found {
		t.Fatalf("a setting before any file: found %v, err %v", found, err)
	}

	for key, value := range map[string]string{"max_turns": "150", "compact_at": "80%", "default_model": "org/model: big"} {
		if err := files.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}

	for _, line := range []string{"system:\n", "  max_turns: 150\n", "  compact_at: 80%\n", "  default_model: 'org/model: big'\n"} {
		if !strings.Contains(written(t, files), line) {
			t.Errorf("no line %q in:\n%s", line, written(t, files))
		}
	}

	settings, err := files.Settings()

	if err != nil || len(settings) != 3 || settings["max_turns"] != "150" || settings["default_model"] != "org/model: big" {
		t.Fatalf("settings = %v, err = %v", settings, err)
	}

	// A value that would read as nothing is still a value.
	files.SetSetting("default_model", "null")

	if value, found, _ := files.Setting("default_model"); !found || value != "null" {
		t.Fatalf("a model called null = %q, found %v", value, found)
	}

	if err := files.DeleteSetting("default_model"); err != nil {
		t.Fatal(err)
	}

	if err := files.DeleteSetting("default_model"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting twice: err = %v", err)
	}

	// The section goes with its last setting.
	files.DeleteSetting("max_turns")
	files.DeleteSetting("compact_at")

	if left := written(t, files); strings.Contains(left, "system") {
		t.Fatalf("an empty section was left:\n%s", left)
	}
}

// What a person wrote stays as they wrote it when rumbo changes something
// else: the comments, and the values it does not touch.
func TestTheFileKeepsWhatAPersonWrote(t *testing.T) {
	files := testFiles(t)

	os.WriteFile(files.config, []byte(`# what I use
system:
  default_provider: local # at home
  max_turns:

providers:
  local:
    command: [ollama-provider, --fast]
    config:
      # where it listens
      base_url: http://localhost:11434/v1
      context_window: 32768
`), 0o600)

	// A key with nothing after it is not set.
	if _, found, _ := files.Setting("max_turns"); found {
		t.Fatal("a key with no value was read as a setting")
	}

	files.SetSetting("max_turns", "90")

	local, err := files.Program(KindProvider, "local")

	if err != nil || len(local.Command) != 2 || string(local.Config) != `{"base_url":"http://localhost:11434/v1","context_window":32768}` {
		t.Fatalf("local = %+v, err = %v", local, err)
	}

	local.Config = json.RawMessage(`{"base_url":"http://localhost:11434/v1","context_window":65536,"effort":"3","stream":true}`)

	if err := files.PutProgram(KindProvider, local); err != nil {
		t.Fatal(err)
	}

	for _, line := range []string{
		"# what I use\n",
		"  default_provider: local # at home\n",
		"  max_turns: 90\n",
		"    command: [ollama-provider, --fast]\n",
		"      # where it listens\n",
		"      base_url: http://localhost:11434/v1\n",
		"      context_window: 65536\n",
		"      effort: \"3\"\n",
		"      stream: true\n",
	} {
		if !strings.Contains(written(t, files), line) {
			t.Errorf("no line %q in:\n%s", line, written(t, files))
		}
	}

	// What was written reads back as the same JSON, types included.
	if again, _ := files.Program(KindProvider, "local"); string(again.Config) != `{"base_url":"http://localhost:11434/v1","context_window":65536,"effort":"3","stream":true}` {
		t.Fatalf("config = %s", again.Config)
	}

	// A file that is a link stays one, as it is among someone's dotfiles.
	linked := Open(filepath.Join(t.TempDir(), "config.yaml"), files.data)

	if err := os.Symlink(files.config, linked.config); err != nil {
		t.Fatal(err)
	}

	linked.SetSetting("frontend", "plain")

	if info, err := os.Lstat(linked.config); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v, err = %v", info.Mode(), err)
	}

	if value, _, _ := files.Setting("frontend"); value != "plain" {
		t.Fatalf("the file linked to has frontend = %q", value)
	}
}

func TestProgramsAreKeptByKind(t *testing.T) {
	files := testFiles(t)

	program := Program{Name: "mine", Command: []string{"run", "--fast", "3", "true", "two words"}}

	if err := files.PutProgram(KindFrontend, program); err != nil {
		t.Fatal(err)
	}

	if _, err := files.Program(KindProvider, "mine"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a front end was found as a provider: %v", err)
	}

	found, err := files.Program(KindFrontend, "mine")

	if err != nil || strings.Join(found.Command, "|") != "run|--fast|3|true|two words" || found.Config != nil {
		t.Fatalf("found = %+v, err = %v", found, err)
	}

	// One rumbo has built in is registered by what was chosen for it alone:
	// its model, and its configuration.
	files.PutProgram(KindProvider, Program{Name: "openai", Model: "gpt-x", Config: json.RawMessage(`{"effort":"high","nested":{"depth":2}}`)})

	if !strings.Contains(written(t, files), "  openai:\n    model: gpt-x\n    config:\n") {
		t.Fatalf("the model is not written beside the configuration:\n%s", written(t, files))
	}
	files.PutProgram(KindProvider, Program{Name: "another", Command: []string{"run"}})

	providers, err := files.Programs(KindProvider)

	if err != nil || len(providers) != 2 || providers[0].Name != "another" || providers[1].Command != nil || providers[1].Model != "gpt-x" ||
		string(providers[1].Config) != `{"effort":"high","nested":{"depth":2}}` {
		t.Fatalf("providers = %+v, err = %v", providers, err)
	}

	// A model set after the configuration still goes before it.
	files.PutProgram(KindProvider, Program{Name: "another", Command: []string{"run"}, Model: "big", Config: json.RawMessage(`{"a":1}`)})
	files.PutProgram(KindProvider, Program{Name: "another", Command: []string{"run"}, Config: json.RawMessage(`{"a":1}`)})
	files.PutProgram(KindProvider, Program{Name: "another", Command: []string{"run"}, Model: "small", Config: json.RawMessage(`{"a":1}`)})

	if !strings.Contains(written(t, files), "  another:\n    command: [run]\n    model: small\n    config:\n      a: 1\n") {
		t.Fatalf("the model is not between the command and the configuration:\n%s", written(t, files))
	}

	files.PutProgram(KindProvider, Program{Name: "another", Command: []string{"run"}})
	files.PutProgram(KindProvider, Program{Name: "openai", Config: json.RawMessage(`{"nested":{"depth":3}}`)})

	if changed, _ := files.Program(KindProvider, "openai"); string(changed.Config) != `{"nested":{"depth":3}}` || changed.Model != "" {
		t.Fatalf("after a change, openai = %+v", changed)
	}

	if strings.Contains(written(t, files), "model") {
		t.Fatalf("a model taken out is still written:\n%s", written(t, files))
	}

	if err := files.DeleteProgram(KindFrontend, "mine"); err != nil {
		t.Fatal(err)
	}

	if err := files.DeleteProgram(KindFrontend, "mine"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting twice: err = %v", err)
	}

	if left := written(t, files); strings.Contains(left, "frontends") {
		t.Fatalf("an empty section was left:\n%s", left)
	}

	if err := files.PutProgram("plugin", program); err == nil {
		t.Fatal("a program of an unknown kind was accepted")
	}

	if err := files.PutProgram(KindProvider, Program{Name: "bad", Config: json.RawMessage(`"text"`)}); err == nil {
		t.Fatal("a configuration that is no object was accepted")
	}
}

// A file rumbo would read something else from than its writer meant is
// refused, saying where: nothing is guessed, and nothing is written over it.
func TestAFileRumboCannotReadSaysWhere(t *testing.T) {
	for content, want := range map[string]string{
		"system:\n  max_turns: [1\n":                                    "did not find expected",
		"sistem:\n  max_turns: 1\n":                                     `line 1: "sistem" is not something rumbo reads`,
		"system:\n  max_turns: [1, 2]\n":                                "line 2: the system setting max_turns takes one value",
		"system: 3\n":                                                   "line 1: system holds names with their values",
		"providers:\n  mine:\n    comand: [run]\n":                      `line 3: "comand" is not something rumbo reads of mine`,
		"providers:\n  mine:\n    command: run --fast\n":                "line 3: the command of mine is a list of words",
		"providers:\n  mine:\n    config: fast\n":                       "line 3: the config of mine holds fields",
		"providers:\n  mine:\n    model: [a, b]\n":                      "line 3: the model of mine is one name",
		"frontends:\n  tui:\n    model: big\n":                          `line 3: "model" is not something rumbo reads of tui; it reads command and config`,
		"frontends:\n  mine: run\n":                                     "line 2: mine holds its command and config, each on a line of its own",
		"extensions:\n  mine:\n    replaces: bash\n":                    "line 3: what mine replaces is a list of tools, such as [bash]",
		"providers:\n  mine:\n    replaces: [bash]\n":                   `line 3: "replaces" is not something rumbo reads of mine; it reads command, model and config`,
		"plugins:\n  mine:\n    sorce: /a\n":                            `line 3: "sorce" is not something rumbo reads of mine; it reads source and replaces`,
		"plugins:\n  mine: /a\n":                                        "line 2: mine holds its source and replaces",
		"- system\n":                                                    "line 1: it holds system, providers, frontends, extensions, and plugins",
		"system:\n  max_turns: 1\nsystem:\n  max_turns: 2\n":            "line 3",
		"providers:\n  a: {command: [x]}\n  a: {command: [y]}\n":        "line 3",
		"providers:\n  mine:\n    config:\n      ? [a, b]\n      : 1\n": "the config of mine",
	} {
		files := testFiles(t)
		os.WriteFile(files.config, []byte(content), 0o600)

		_, _, err := files.Setting("max_turns")

		if err == nil {
			_, err = files.Programs(KindProvider)
		}

		if err == nil || !strings.Contains(err.Error(), files.config) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q in the file named", content, err, want)
		}

		if err := files.SetSetting("max_turns", "9"); err == nil {
			t.Errorf("%q: a setting was written over it", content)
		}

		if written(t, files) != content {
			t.Errorf("%q was changed to %q", content, written(t, files))
		}
	}

	// A file with nothing but a comment is an empty one, and keeps it.
	files := testFiles(t)
	os.WriteFile(files.config, []byte("# nothing chosen yet\n"), 0o600)

	if err := files.SetSetting("max_turns", "9"); err != nil {
		t.Fatal(err)
	}

	if got := written(t, files); got != "# nothing chosen yet\nsystem:\n  max_turns: 9\n" {
		t.Fatalf("the file = %q", got)
	}
}
