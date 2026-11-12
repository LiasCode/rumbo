package ambient

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The model is told where it is and what the user's instructions are, and
// on later runs only what changed.
func TestChangesReportsOnlyWhatChanged(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

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
