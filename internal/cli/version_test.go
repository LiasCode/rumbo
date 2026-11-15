package cli

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestMistypedCommandsAreNotTasks(t *testing.T) {
	for word, meant := range map[string]string{
		"versoin":  "version",
		"Version":  "version",
		"sesions":  "sessions",
		"lgo":      "log",
		"sytsem":   "system",
		"provdier": "provider",
	} {
		if command, ok := mistyped(word); !ok || command != meant {
			t.Errorf("mistyped(%q) = %q, %v; want %q", word, command, ok, meant)
		}
	}

	for _, word := range []string{"hola", "go", "fix", "summarize", "-c", "two words", "deploy", ""} {
		if command, ok := mistyped(word); ok {
			t.Errorf("mistyped(%q) took it for %q", word, command)
		}
	}
}

// The guard answers before anything is opened, and -- lets the word through
// as a task.
func TestDispatchRefusesAMistypedCommand(t *testing.T) {
	t.Setenv("RUMBO_DB", t.TempDir()+"/never-opened.db")

	err := dispatch([]string{"versoin"})

	if err == nil || !strings.Contains(err.Error(), `did you mean "version"`) || !strings.Contains(err.Error(), "rumbo -- versoin") {
		t.Fatalf("dispatch(versoin) = %v", err)
	}

	// The command's old name is not sent to the model either.
	if err := dispatch([]string{"config", "set", "model", "anthropic/x"}); err == nil || !strings.Contains(err.Error(), `now "rumbo system"`) {
		t.Fatalf("dispatch(config ...) = %v", err)
	}
}

func TestVersionNamesTheBuild(t *testing.T) {
	fromSource := &debug.BuildInfo{
		Main:     debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "3771402e789b0123"}, {Key: "vcs.modified", Value: "true"}},
	}

	for _, test := range []struct {
		release string
		info    *debug.BuildInfo
		want    string
	}{
		{"0.1.0", fromSource, "0.1.0"},
		{"", &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, "0.1.0"},
		{"", fromSource, "development build of 3771402e789b with local changes"},
		{"", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "development build"},
		{"", nil, "development build"},
	} {
		if got := built(test.release, test.info); got != test.want {
			t.Errorf("built(%q) = %q, want %q", test.release, got, test.want)
		}
	}
}

func TestVersionSaysTheProtocol(t *testing.T) {
	if described := describeVersion(); !strings.HasPrefix(described, "rumbo ") || !strings.HasSuffix(described, "(protocol 1)") {
		t.Fatalf("describeVersion() = %q", described)
	}

	version = "0.1.0"
	t.Cleanup(func() { version = "" })

	if described := describeVersion(); described != "rumbo 0.1.0 (protocol 1)" {
		t.Fatalf("describeVersion() = %q", described)
	}
}
