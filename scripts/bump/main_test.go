package main

import (
	"strings"
	"testing"
)

func TestLevelOfACommit(t *testing.T) {
	for message, want := range map[string]level{
		"feat: paste an image":                      minor,
		"feat(tui): paste an image":                 minor,
		"fix: keep the keymap":                      patch,
		"perf: read less":                           patch,
		"docs: say where skills go":                 none,
		"chore: release 0.7.0":                      none,
		"feat!: drop the old protocol":              major,
		"fix(store)!: rename a file":                major,
		"refactor: move\n\nBREAKING CHANGE: gone":   major,
		"feat: add\n\nBREAKING-CHANGE: renamed":     major,
		"Merge branch 'main'":                       none,
		"feature: not a type this tool knows":       none,
		"feat:no space after the colon":             none,
		"docs: mention\n\nnot a BREAKING CHANGE: x": none,
	} {
		if got := levelOf(message); got != want {
			t.Errorf("levelOf(%q) = %s, want %s", message, got, want)
		}
	}
}

func TestRaiseFollowsSemanticVersioning(t *testing.T) {
	for _, test := range []struct {
		from  version
		level level
		want  string
	}{
		{version{0, 7, 0}, patch, "0.7.1"},
		{version{0, 7, 3}, minor, "0.8.0"},
		{version{0, 7, 3}, major, "0.8.0"},
		{version{1, 2, 3}, major, "2.0.0"},
		{version{1, 2, 3}, minor, "1.3.0"},
		{version{1, 2, 3}, none, "1.2.3"},
	} {
		if got := test.from.raise(test.level).String(); got != test.want {
			t.Errorf("%s raised by %s = %s, want %s", test.from, test.level, got, test.want)
		}
	}
}

func TestParseVersion(t *testing.T) {
	if v, err := parseVersion("v0.7.0"); err != nil || v != (version{0, 7, 0}) {
		t.Errorf("parseVersion(v0.7.0) = %v, %v", v, err)
	}

	if _, err := parseVersion("release-1"); err == nil {
		t.Error("parseVersion(release-1) took it for a version")
	}
}

func TestDatedKeepsUnreleasedForWhatComesNext(t *testing.T) {
	changelog := "# Changelog\n\n## Unreleased\n\n- A change.\n\n## 0.7.0 (2026-11-21)\n\n- Older.\n"

	got, err := dated(changelog, version{0, 8, 0}, "2026-11-23")
	if err != nil {
		t.Fatal(err)
	}

	want := "# Changelog\n\n## Unreleased\n\n## 0.8.0 (2026-11-23)\n\n- A change.\n\n## 0.7.0 (2026-11-21)\n\n- Older.\n"
	if got != want {
		t.Fatalf("dated:\n%s\nwant:\n%s", got, want)
	}
}

func TestDatedRefusesNothingToSay(t *testing.T) {
	for _, changelog := range []string{
		"# Changelog\n\n## Unreleased\n\n## 0.7.0 (2026-11-21)\n\n- Older.\n",
		"# Changelog\n\n## Unreleased\n\n",
		"# Changelog\n\n## 0.7.0 (2026-11-21)\n\n- Older.\n",
	} {
		if _, err := dated(changelog, version{0, 8, 0}, "2026-11-23"); err == nil || !strings.Contains(err.Error(), "Unreleased") {
			t.Errorf("dated(%q) = %v, want a refusal", changelog, err)
		}
	}
}
