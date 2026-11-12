package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func call(t *testing.T, target Tool, dir string, args map[string]any) (string, error) {
	t.Helper()

	raw, _ := json.Marshal(args)

	return target.Run(context.Background(), dir, raw)
}

func TestEditRequiresUniqueMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	os.WriteFile(path, []byte("one\ntwo\none\n"), 0o644)

	if _, err := call(t, Edit{}, dir, map[string]any{"path": "a.txt", "old": "one", "new": "1"}); err == nil {
		t.Fatal("ambiguous edit should fail")
	}

	if _, err := call(t, Edit{}, dir, map[string]any{"path": "a.txt", "old": "two", "new": "2"}); err != nil {
		t.Fatal(err)
	}

	if content, _ := os.ReadFile(path); string(content) != "one\n2\none\n" {
		t.Fatalf("content = %q", content)
	}
}

func TestReadWindows(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1\n2\n3\n4\n"), 0o644)

	output, err := call(t, Read{}, dir, map[string]any{"path": "a.txt", "offset": 2, "limit": 2})

	if err != nil {
		t.Fatal(err)
	}

	if output != "2\n3\n[lines 2-3 of 4; continue with offset 4]" {
		t.Fatalf("output = %q", output)
	}
}

func TestBashReportsFailureAndKeepsTail(t *testing.T) {
	dir := t.TempDir()

	_, err := call(t, Bash{}, dir, map[string]any{"command": "echo before; exit 3"})

	if err == nil || !strings.Contains(err.Error(), "before") || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("err = %v", err)
	}

	output, err := call(t, Bash{}, dir, map[string]any{"command": "seq 1 5000"})

	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(output, "[output truncated: last 1500 of 5000 lines]") || !strings.HasSuffix(output, "\n5000") {
		t.Fatalf("unexpected truncation: %q ... %q", output[:60], output[len(output)-20:])
	}
}

func TestBashTimeoutKillsProcessGroup(t *testing.T) {
	_, err := call(t, Bash{}, t.TempDir(), map[string]any{"command": "sleep 30 & sleep 30", "timeout": 1})

	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
}
