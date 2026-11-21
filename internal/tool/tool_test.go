package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

func TestBashBackgroundReturnsAtOnceAndIsStoppedOnClose(t *testing.T) {
	dir := t.TempDir()
	set := NewSet()
	bash := set.Tools()[3]

	defer set.Close()

	started := time.Now()
	output, err := call(t, bash, dir, map[string]any{"command": "echo serving; sleep 60", "background": true})

	if err != nil || time.Since(started) > 5*time.Second {
		t.Fatalf("err = %v after %s", err, time.Since(started))
	}

	var group int

	if _, err := fmt.Sscanf(output, "started in the background as process group %d.", &group); err != nil {
		t.Fatalf("output = %q", output)
	}

	_, after, _ := strings.Cut(output, "Its output is in ")
	log, _, _ := strings.Cut(after, ". Stop it")

	if content, _ := os.ReadFile(log); string(content) != "serving\n" {
		t.Fatalf("log = %q", content)
	}

	if syscall.Kill(-group, 0) != nil {
		t.Fatal("the background process is not running")
	}

	set.Close()
	time.Sleep(100 * time.Millisecond)

	if syscall.Kill(-group, 0) == nil {
		t.Fatal("the background process survived the close")
	}

	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("the log of a stopped process is still there: %v", err)
	}

	// A command that fails at once is reported as a failure, not as started.
	_, err = call(t, bash, dir, map[string]any{"command": "no-such-command-here", "background": true})

	if err == nil || !strings.Contains(err.Error(), "ended at once") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
}

// Another session's model has not read what this one did, so it may not
// replace the file whole; both still share the processes left running.
func TestSeparateSetsKeepTheirOwnReads(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("the user's notes\n"), 0o644)

	first := NewSet()
	second := first.Separate()
	read := first.Tools()[0]
	write := second.Tools()[1]

	if _, err := call(t, read, dir, map[string]any{"path": "notes.txt"}); err != nil {
		t.Fatal(err)
	}

	if _, err := call(t, write, dir, map[string]any{"path": "notes.txt", "content": "mine"}); err == nil || !strings.Contains(err.Error(), "have not read it") {
		t.Fatalf("a session overwrote a file only another one read: err = %v", err)
	}

	if first.jobs != second.jobs {
		t.Fatal("the sets do not share their background processes")
	}
}

// What the tools save for the model goes with the host: the whole of a long
// output is kept in a directory of the set's own until the set is closed,
// and then nothing is left.
func TestSavedOutputIsRemovedWithTheSet(t *testing.T) {
	set := NewSet()
	bash := set.Tools()[3]

	defer set.Close()

	output, err := call(t, bash, t.TempDir(), map[string]any{"command": "seq 1 20000"})

	if err != nil {
		t.Fatal(err)
	}

	_, after, found := strings.Cut(output, " lines, is in ")
	path, _, _ := strings.Cut(after, ";")

	if !found || !strings.HasPrefix(filepath.Base(filepath.Dir(path)), "rumbo-") {
		t.Fatalf("the output does not say where the rest is: %q", path)
	}

	if content, err := os.ReadFile(path); err != nil || !strings.HasSuffix(string(content), "\n20000") {
		t.Fatalf("the saved output: err = %v", err)
	}

	set.Close()

	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("the saved output is still there after close: %v", err)
	}
}
