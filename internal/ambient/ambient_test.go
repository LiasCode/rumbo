package ambient

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// userConfig gives the test a configuration directory of its own, and
// returns it: XDG_CONFIG_HOME on Linux, and one under HOME on macOS, which
// does not read XDG_CONFIG_HOME.
func userConfig(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	config, err := os.UserConfigDir()

	if err != nil {
		t.Fatal(err)
	}

	return config
}

// The model is told where it is and what the user's instructions are, and
// on later runs only what changed.
func TestChangesReportsOnlyWhatChanged(t *testing.T) {
	userConfig(t)

	dir := t.TempDir()
	ctx := context.Background()
	known := map[string]string{}

	first := Changes(ctx, dir, Defaults(), known)

	if len(first) != 1 || first[0].Name != "environment" || !strings.Contains(first[0].Text, "working directory: "+dir) {
		t.Fatalf("without instructions only the environment is sent, got %+v", first)
	}

	known[first[0].Name] = first[0].Text

	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("Answer in haiku.\n"), 0o644)
	second := Changes(ctx, dir, Defaults(), known)

	if len(second) != 1 || second[0].Name != "instructions" || !strings.Contains(second[0].Text, "Answer in haiku.") {
		t.Fatalf("a new instructions file is the only change, got %+v", second)
	}

	known[second[0].Name] = second[0].Text

	if again := Changes(ctx, dir, Defaults(), known); len(again) != 0 {
		t.Fatalf("nothing changed, yet %+v was sent", again)
	}

	// A removed section is reported with empty text, so the model is told.
	os.Remove(filepath.Join(dir, "AGENTS.md"))

	if removed := Changes(ctx, dir, Defaults(), known); len(removed) != 1 || removed[0].Text != "" {
		t.Fatalf("removal: %+v", removed)
	}
}

// Instructions too long to send with every request are cut at a line, and
// say where the rest is.
func TestLongInstructionsAreBounded(t *testing.T) {
	long := strings.Repeat("Keep every rule in mind.\n", 2000)
	got := bounded("/repo/AGENTS.md", strings.TrimSpace(long))

	if len(got) > maxInstructionBytes+300 || !strings.Contains(got, "Read the rest of /repo/AGENTS.md") {
		t.Fatalf("bounded to %d bytes, ending %q", len(got), got[len(got)-120:])
	}

	if cut := got[:strings.Index(got, "\n[These instructions continue")]; !strings.HasSuffix(cut, "Keep every rule in mind.") {
		t.Fatalf("cut in the middle of a line: %q", cut[len(cut)-40:])
	}

	if short := bounded("/repo/AGENTS.md", "Be brief."); short != "Be brief." {
		t.Fatalf("short instructions were changed: %q", short)
	}
}

// A section whose collector is gone, as when the user takes out the
// extension that had it, is removed.
func TestASectionNoCollectorHasIsRemoved(t *testing.T) {
	collectors := []Collector{Func("kept", func(context.Context, string) string { return "same" })}
	known := map[string]string{"kept": "same", "zeta": "old", "alpha": "old"}

	changes := Changes(context.Background(), t.TempDir(), collectors, known)

	if len(changes) != 2 || changes[0].Name != "alpha" || changes[1].Name != "zeta" || changes[0].Text != "" || changes[1].Text != "" {
		t.Fatalf("changes = %+v", changes)
	}
}
