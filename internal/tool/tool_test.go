package tool

import (
	"context"
	"encoding/json"
	"fmt"
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

	// Long output keeps its beginning and its end, and the rest is in a file.
	if len(output) > outputBytes+300 || !strings.HasPrefix(output, "1\n2\n") || !strings.HasSuffix(output, "\n5000") {
		t.Fatalf("unexpected shortening: %d bytes, %q ... %q", len(output), output[:20], output[len(output)-20:])
	}

	_, after, found := strings.Cut(output, "lines, is in ")
	path, _, _ := strings.Cut(after, ";")

	if !found {
		t.Fatalf("no saved file named in %q", output[outputBytes/4-200:outputBytes/4+300])
	}

	defer os.Remove(path)

	saved, err := os.ReadFile(path)

	if err != nil || strings.Count(string(saved), "\n") != 4999 {
		t.Fatalf("saved output: %d lines, err = %v, want all 5000", strings.Count(string(saved), "\n")+1, err)
	}

	// Every line break but the one after the marker ends a line of output.
	shown := strings.Count(output, "\n")
	var omitted int
	fmt.Sscanf(output[strings.Index(output, "[... ")+5:], "%d", &omitted)

	if shown+omitted != 5000 {
		t.Fatalf("%d lines shown and %d reported omitted do not add up to 5000", shown, omitted)
	}
}

func TestReadDefaultsToASmallWindow(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Repeat("line\n", 3000)), 0o644)

	output, err := call(t, Read{}, dir, map[string]any{"path": "big.txt"})

	if err != nil || !strings.HasSuffix(output, "[lines 1-500 of 3000; continue with offset 501]") {
		t.Fatalf("default window: err = %v, tail = %q", err, output[len(output)-60:])
	}

	output, _ = call(t, Read{}, dir, map[string]any{"path": "big.txt", "limit": 9000})

	if !strings.HasSuffix(output, "[lines 1-2000 of 3000; continue with offset 2001]") {
		t.Fatalf("a large limit is capped: tail = %q", output[len(output)-60:])
	}
}

func TestBashTimeoutKillsProcessGroup(t *testing.T) {
	_, err := call(t, Bash{}, t.TempDir(), map[string]any{"command": "sleep 30 & sleep 30", "timeout": 1})

	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
}
