package cli

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/LiasCode/rumbo/protocol"
)

// A host says the processes it has: itself, its provider's, its
// extensions', and what the model left running, each with what it uses.
func TestAHostSaysTheProcessesItHas(t *testing.T) {
	h := testHost(t)
	withClaude(t, h.database, "")
	call(t, h, "use", `{"provider":"claude","model":"claude-x"}`)

	if _, err := registerExtension(h.database, "finder", extendWith("find"), nil, nil, io.Discard); err != nil {
		t.Fatal(err)
	}

	// Describing starts the provider and the extensions, as a run does.
	if _, err := h.describe(context.Background()); err != nil {
		t.Fatal(err)
	}

	bash := h.tools.Tools()[3]

	if _, err := bash.Run(context.Background(), t.TempDir(), []byte(`{"command":"sleep 60","background":true}`)); err != nil {
		t.Fatal(err)
	}

	result, err := h.processes(context.Background())

	if err != nil {
		t.Fatal(err)
	}

	if first := result.Processes[0]; first.PID != os.Getpid() || first.Role != protocol.ProcessHost || first.Memory <= 0 {
		t.Fatalf("the host is not first, with what it uses: %+v", result.Processes)
	}

	found := map[string]protocol.ProcessUsage{}

	for _, process := range result.Processes[1:] {
		found[strings.TrimSpace(process.Role+" "+process.Name)] = process
	}

	for _, what := range []string{"provider claude", "extension finder", "background"} {
		if process, there := found[what]; !there || process.Parent != os.Getpid() || process.Memory <= 0 {
			t.Fatalf("%s is not among the host's processes: %+v", what, result.Processes)
		}
	}

	shown := call(t, h, "processes", "").Text

	for _, what := range []string{"PID", "host", "  provider claude", "  extension finder", "  background", " MB", "processes, at most "} {
		if !strings.Contains(shown, what) {
			t.Fatalf("processes does not show %q:\n%s", what, shown)
		}
	}
}
