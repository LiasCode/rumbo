package cli

import (
	"os"
	"testing"
)

// The test binary doubles as rumbo's built-in providers, which "{self}"
// names in a registered command, so tests can start a real one.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "provide" {
		os.Exit(Main(os.Args[1:]))
	}

	os.Exit(m.Run())
}
