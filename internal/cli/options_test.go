package cli

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/LiasCode/rumbo/internal/store"
)

func TestOptionsAreLayeredFromGlobalToRun(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "test.db"))

	if err != nil {
		t.Fatal(err)
	}

	defer database.Close()

	database.SetSetting(store.GlobalScope, "options.anthropic", `{"effort":"low","verbose":false}`)
	database.SetSetting("/repo", "options.anthropic", `{"effort":"medium"}`)
	database.SetSetting(store.GlobalScope, "options.other", `{"effort":"max"}`)

	stored, err := storedOptions(database, "/repo", "anthropic")

	if err != nil || string(stored) != `{"effort":"medium","verbose":false}` {
		t.Fatalf("stored = %s, err = %v", stored, err)
	}

	var flags optionFlags
	flags.Set("effort=high")
	flags.Set("limit=3")
	flags.Set("note=two words")

	merged, err := mergeOptions(stored, flags.object(), json.RawMessage(`{"verbose":true}`))

	if err != nil || string(merged) != `{"effort":"high","limit":3,"note":"two words","verbose":true}` {
		t.Fatalf("merged = %s, err = %v", merged, err)
	}

	if none, _ := mergeOptions(nil, nil); none != nil {
		t.Fatalf("no options should be no field at all, got %s", none)
	}

	if _, err := mergeOptions(json.RawMessage(`"high"`)); err == nil {
		t.Fatal("options that are not an object were accepted")
	}

	if flags.Set("no-value") == nil {
		t.Fatal("a flag without a value was accepted")
	}
}
