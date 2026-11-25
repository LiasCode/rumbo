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

	result, err := runTool(t, target, dir, args)

	return result.Text, err
}

func runTool(t *testing.T, target Tool, dir string, args map[string]any) (Result, error) {
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

// A file is not replaced whole by a model that does not know what is in it.
func TestWriteNeedsToKnowTheFileItReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	os.WriteFile(path, []byte("the user's notes\n"), 0o644)

	tools := NewSet().Tools()
	read, write, edit := tools[0], tools[1], tools[2]

	if _, err := call(t, write, dir, map[string]any{"path": "notes.txt", "content": "mine"}); err == nil || !strings.Contains(err.Error(), "have not read it") {
		t.Fatalf("overwriting an unread file: err = %v", err)
	}

	if content, _ := os.ReadFile(path); string(content) != "the user's notes\n" {
		t.Fatal("the file was replaced anyway")
	}

	// A new file needs nothing, and can be rewritten by the model that made it.
	for _, content := range []string{"first", "second"} {
		if _, err := call(t, write, dir, map[string]any{"path": "new/file.txt", "content": content}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := call(t, read, dir, map[string]any{"path": "notes.txt"}); err != nil {
		t.Fatal(err)
	}

	// Someone else changes the file after the model read it.
	os.WriteFile(path, []byte("the user's notes, with more\n"), 0o644)

	if _, err := call(t, write, dir, map[string]any{"path": "notes.txt", "content": "mine"}); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("overwriting a file that changed: err = %v", err)
	}

	// An edit is guarded by its exact match, and leaves the file known.
	if _, err := call(t, edit, dir, map[string]any{"path": "notes.txt", "old": "with more", "new": "edited"}); err != nil {
		t.Fatal(err)
	}

	if _, err := call(t, write, dir, map[string]any{"path": "notes.txt", "content": "mine"}); err != nil {
		t.Fatalf("overwriting after an edit: %v", err)
	}
}
