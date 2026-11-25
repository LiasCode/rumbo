//go:build windows

package tool

import (
	"strings"
	"testing"
)

func TestPowerShellRunsACommandAndReportsItsFailure(t *testing.T) {
	output, err := call(t, Bash{}, t.TempDir(), map[string]any{"command": "Write-Output 'café'"})

	if err != nil || output != "café" {
		t.Fatalf("output = %q, err = %v", output, err)
	}

	_, err = call(t, Bash{}, t.TempDir(), map[string]any{"command": "Write-Output before; exit 3"})

	if err == nil || !strings.Contains(err.Error(), "before") || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("err = %v", err)
	}
}

func TestTheShellToolIsNamedAfterPowerShell(t *testing.T) {
	if name := (Bash{}).Def().Name; name != "powershell" {
		t.Fatalf("name = %q", name)
	}
}
