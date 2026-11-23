package plain

import (
	"encoding/json"
	"strings"
	"testing"
)

// plain has no configuration, so a field it is given stops it, saying how
// to take it out; nothing given, or an empty object, is fine.
func TestPlainTakesNoConfiguration(t *testing.T) {
	for _, given := range []string{``, `{}`} {
		if err := refused(json.RawMessage(given)); err != nil {
			t.Errorf("given %q: %v", given, err)
		}
	}

	err := refused(json.RawMessage(`{"theme":"plain"}`))

	if err == nil || !strings.Contains(err.Error(), "theme") || !strings.Contains(err.Error(), "rumbo frontend set plain theme=") {
		t.Fatalf("a field: err = %v", err)
	}
}
