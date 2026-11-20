package cli

import (
	"encoding/json"
	"testing"
)

// Objects are laid over each other field by field, and -o takes name=value.
func TestOptionsAreLaidOverEachOther(t *testing.T) {
	if merged, err := mergeOptions(json.RawMessage(`{"effort":"low","verbose":false}`), json.RawMessage(`{"effort":"high","limit":3}`)); err != nil || string(merged) != `{"effort":"high","limit":3,"verbose":false}` {
		t.Fatalf("merged = %s, err = %v", merged, err)
	}

	if none, _ := mergeOptions(nil, nil); none != nil {
		t.Fatalf("no options should be no field at all, got %s", none)
	}

	if _, err := mergeOptions(json.RawMessage(`"high"`)); err == nil {
		t.Fatal("options that are not an object were accepted")
	}

	var flags optionFlags

	if flags.Set("effort=high") != nil || flags.Set("no-value") == nil {
		t.Fatalf("flags = %v", flags)
	}
}
