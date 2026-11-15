package cli

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/LiasCode/rumbo/internal/store"
)

// A session's options are laid over the provider's, and the command line
// changes the session's one by one.
func TestOptionsAreLayeredFromProviderToSession(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "test.db"))

	if err != nil {
		t.Fatal(err)
	}

	defer database.Close()

	database.SetSetting(store.GlobalScope, "options.anthropic", `{"effort":"low","verbose":false}`)
	database.SetSetting(store.GlobalScope, "options.other", `{"effort":"max"}`)

	// One stored for a project is no longer read.
	database.SetSetting("/repo", "options.anthropic", `{"effort":"medium"}`)

	if stored, err := storedOptions(database, "anthropic"); err != nil || string(stored) != `{"effort":"low","verbose":false}` {
		t.Fatalf("stored = %s, err = %v", stored, err)
	}

	var flags optionFlags
	flags.Set("effort=high")
	flags.Set("limit=3")
	flags.Set("note=two words")
	flags.Set("verbose=")

	session, err := changeObject(json.RawMessage(`{"verbose":true,"web":true}`), flags)

	if err != nil || string(session) != `{"effort":"high","limit":3,"note":"two words","web":true}` {
		t.Fatalf("session = %s, err = %v", session, err)
	}

	if inForce, err := optionsInForce(database, "anthropic", session); err != nil || string(inForce) != `{"effort":"high","limit":3,"note":"two words","verbose":false,"web":true}` {
		t.Fatalf("in force = %s, err = %v", inForce, err)
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
