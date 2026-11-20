package cli

import (
	"encoding/json"
	"testing"
)

// The command line changes the options chosen in a session one by one, and
// objects are laid over each other field by field.
func TestOptionsAreChangedOneByOne(t *testing.T) {
	var flags optionFlags
	flags.Set("effort=high")
	flags.Set("limit=3")
	flags.Set("note=two words")
	flags.Set("verbose=")

	session, err := changeObject(json.RawMessage(`{"verbose":true,"web":true}`), flags)

	if err != nil || string(session) != `{"effort":"high","limit":3,"note":"two words","web":true}` {
		t.Fatalf("session = %s, err = %v", session, err)
	}

	if merged, err := mergeOptions(json.RawMessage(`{"effort":"low","verbose":false}`), session); err != nil || string(merged) != `{"effort":"high","limit":3,"note":"two words","verbose":false,"web":true}` {
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
