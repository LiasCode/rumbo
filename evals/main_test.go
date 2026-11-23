package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A run reaches the credentials the providers keep in the user's
// configuration directory, linked from where they are, and none of the rest
// of it, such as the user's own instructions.
func TestARunSharesOnlyTheCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if before, set := os.LookupEnv("ANTHROPIC_CONFIG_DIR"); set {
		os.Unsetenv("ANTHROPIC_CONFIG_DIR")
		t.Cleanup(func() { os.Setenv("ANTHROPIC_CONFIG_DIR", before) })
	}

	user, err := os.UserConfigDir()

	if err != nil {
		t.Fatal(err)
	}

	for path, text := range map[string]string{
		filepath.Join(user, "rumbo", "credentials", "chatgpt.json"):         "sign-in",
		filepath.Join(user, "rumbo", "AGENTS.md"):                           "the user's own",
		filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "anthropic", "profile"): "ant auth login",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	workdir := t.TempDir()

	if err := shareCredentials(workdir); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]string{
		filepath.Join(configHome(workdir), "rumbo", "credentials", "chatgpt.json"): "sign-in",
		filepath.Join(configHome(workdir), "anthropic", "profile"):                 "ant auth login",
	} {
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", path, got, err, want)
		}
	}

	if _, err := os.Stat(filepath.Join(configHome(workdir), "rumbo", "AGENTS.md")); !os.IsNotExist(err) {
		t.Errorf("the user's instructions reach the run: %v", err)
	}
}
