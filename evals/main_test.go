package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LiasCode/rumbo/internal/ambient"
)

func TestRunIsolatesGlobalContextWithoutMovingProviderConfiguration(t *testing.T) {
	user := t.TempDir()
	t.Setenv("RUMBO_HOME", user)
	t.Setenv("RUMBO_AMBIENT_DIR", "")

	project := t.TempDir()

	for path, text := range map[string]string{
		filepath.Join(user, "AGENTS.md"):                                 "Global instructions",
		filepath.Join(user, "skills", "global", "SKILL.md"):              "---\nname: global\ndescription: Global skill\n---\n",
		filepath.Join(project, "AGENTS.md"):                              "Project instructions",
		filepath.Join(project, ".agents", "skills", "local", "SKILL.md"): "---\nname: local\ndescription: Project skill\n---\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	collect := func() map[string]string {
		sections := map[string]string{}

		for _, section := range ambient.Changes(context.Background(), project, ambient.Defaults(), nil) {
			sections[section.Name] = section.Text
		}

		return sections
	}

	before := collect()

	if !strings.Contains(before["instructions"], "Global instructions") || !strings.Contains(before["skills"], "Global skill") {
		t.Fatalf("global context missing from baseline: %v", before)
	}

	for _, binding := range environment("example", t.TempDir()) {
		name, value, _ := strings.Cut(binding, "=")

		switch name {
		case "RUMBO_HOME", "RUMBO_AMBIENT_DIR":
			t.Setenv(name, value)
		}
	}

	after := collect()

	if !strings.Contains(after["instructions"], "Project instructions") || !strings.Contains(after["skills"], "Project skill") ||
		strings.Contains(after["instructions"], "Global instructions") || strings.Contains(after["skills"], "Global skill") {
		t.Fatalf("expected only project context: %v", after)
	}

	if os.Getenv("RUMBO_HOME") != user {
		t.Fatal("evaluation moved the provider configuration directories")
	}
}
