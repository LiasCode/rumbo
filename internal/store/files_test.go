package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// testFiles is a store in directories of the test's own.
func testFiles(t *testing.T) *Files {
	t.Helper()

	return Open(filepath.Join(t.TempDir(), "config.yaml"), t.TempDir())
}

// Two processes are two stores on the same files.
func TestLockSessionExcludesOtherProcesses(t *testing.T) {
	first := testFiles(t)
	second := Open(first.config, first.data)

	session, err := first.CreateSession("/project", "test")

	if err != nil {
		t.Fatal(err)
	}

	release, err := first.LockSession(session.ID)

	if err != nil {
		t.Fatal(err)
	}

	// Whether a session is in use is seen from any process, without taking
	// it, as a listing does.
	for _, files := range []*Files{first, second} {
		if used, err := files.SessionInUse(session.ID); err != nil || !used {
			t.Fatalf("a held session in use: %v, %v", used, err)
		}
	}

	if _, err := second.LockSession(session.ID); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock: err = %v, want ErrLocked", err)
	}

	other, _ := first.CreateSession("/project", "other")

	if used, err := second.SessionInUse(other.ID); err != nil || used {
		t.Fatalf("a session never held in use: %v, %v", used, err)
	}

	if unlock, err := second.LockSession(other.ID); err != nil {
		t.Fatalf("a different session should be free: %v", err)
	} else {
		unlock()
	}

	release()

	if used, err := second.SessionInUse(session.ID); err != nil || used {
		t.Fatalf("a released session in use: %v, %v", used, err)
	}

	if unlock, err := second.LockSession(session.ID); err != nil {
		t.Fatalf("lock after release: %v", err)
	} else {
		unlock()
	}

	if _, err := second.LockSession("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("locking a session that does not exist: err = %v", err)
	}
}

// A session is two files a person can read: what it is, and its entries,
// one to a line, as rumbo log prints them.
func TestASessionIsFilesAPersonCanRead(t *testing.T) {
	files := testFiles(t)

	session, err := files.CreateSession("/repo/pkg", "fix the parser")

	if err != nil {
		t.Fatal(err)
	}

	files.SetSessionModel(session.ID, "anthropic", "claude-opus-5-5")
	files.SetSessionOptions(session.ID, json.RawMessage(`{"effort":"high"}`))
	files.Append(session.ID, "user", map[string]string{"text": "two\nlines"})

	dir := filepath.Join(files.data, "sessions", session.ID)
	record, err := os.ReadFile(filepath.Join(dir, "session.json"))

	if err != nil {
		t.Fatal(err)
	}

	for _, line := range []string{
		`  "dir": "/repo/pkg",`,
		`  "title": "fix the parser",`,
		`  "provider": "anthropic",`,
		`  "model": "claude-opus-5-5",`,
		`    "effort": "high"`,
	} {
		if !strings.Contains(string(record), line+"\n") {
			t.Errorf("session.json has no line %q:\n%s", line, record)
		}
	}

	entries, err := os.ReadFile(filepath.Join(dir, "entries.jsonl"))

	if err != nil {
		t.Fatal(err)
	}

	if lines := strings.Split(string(entries), "\n"); len(lines) != 2 || lines[1] != "" ||
		!strings.HasPrefix(lines[0], `{"seq":1,"time":"`) || !strings.HasSuffix(lines[0], `Z","kind":"user","payload":{"text":"two\nlines"}}`) {
		t.Fatalf("entries.jsonl = %q", entries)
	}

	// Only the person who started rumbo reads what a session holds.
	for _, name := range []string{".", "session.json", "entries.jsonl"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s: mode %v, err %v", name, info.Mode(), err)
		}
	}

	// A session made when sessions were kept by the top of a git repository
	// says which that was. It is read as it is, and is its directory's.
	withProject := strings.Replace(string(record), `"dir": "/repo/pkg",`, `"dir": "/repo/pkg",`+"\n"+`  "project": "/repo",`, 1)

	if err := os.WriteFile(filepath.Join(dir, "session.json"), []byte(withProject), 0o600); err != nil {
		t.Fatal(err)
	}

	if earlier, err := files.Sessions("/repo/pkg"); err != nil || len(earlier) != 1 || earlier[0].ID != session.ID || earlier[0].Model != "claude-opus-5-5" {
		t.Fatalf("a session that names a project = %+v, err = %v", earlier, err)
	}

	// Another process numbers the next entry after the ones that are there.
	other := Open(files.config, files.data)

	if next, err := other.Append(session.ID, "assistant", map[string]string{}); err != nil || next.Seq != 2 {
		t.Fatalf("the next entry is %d, err = %v", next.Seq, err)
	}

	if next, err := files.Append(session.ID, "user", map[string]string{}); err != nil || next.Seq != 3 {
		t.Fatalf("the entry after another process's is %d, err = %v", next.Seq, err)
	}

	// A name that is a path is no session's.
	for _, id := range []string{"", "..", "../" + session.ID, ".hidden", "a/b"} {
		if _, err := files.Session(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("session %q: err = %v", id, err)
		}
	}

	if _, err := files.Append("missing", "user", map[string]string{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("appending to a session that does not exist: err = %v", err)
	}
}

// A process that dies while it writes an entry leaves a line without its
// end. It is no entry, and the next one does not go on from the middle of it.
func TestALineCutShortIsNoEntry(t *testing.T) {
	files := testFiles(t)
	session, _ := files.CreateSession("/repo", "cut short")
	files.Append(session.ID, "user", map[string]string{"text": "whole"})

	path := filepath.Join(files.data, "sessions", session.ID, "entries.jsonl")
	log, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)

	if err != nil {
		t.Fatal(err)
	}

	log.WriteString(`{"seq":2,"time":"2026-01-01T00:00:00.000Z","kind":"assis`)
	log.Close()

	for _, reader := range []*Files{files, Open(files.config, files.data)} {
		if entries, err := reader.Entries(session.ID); err != nil || len(entries) != 1 {
			t.Fatalf("entries = %d, err = %v; want the whole one", len(entries), err)
		}
	}

	if next, err := files.Append(session.ID, "assistant", map[string]string{"text": "after"}); err != nil || next.Seq != 2 {
		t.Fatalf("the next entry is %d, err = %v", next.Seq, err)
	}

	entries, err := files.Entries(session.ID)

	if err != nil || len(entries) != 2 || string(entries[1].Payload) != `{"text":"after"}` {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}

	// A line that is whole and not an entry is damage, and says where.
	os.WriteFile(path, []byte("not an entry\n"), 0o600)

	if _, err := files.Entries(session.ID); err == nil || !strings.Contains(err.Error(), path+", line 1") {
		t.Fatalf("a damaged line: err = %v", err)
	}
}

// A session is its directory's alone: one started in a directory inside is
// not listed with it.
func TestSessionsAreFoundByDirectoryAndOrderedByUse(t *testing.T) {
	files := testFiles(t)

	if none, err := files.Sessions(""); err != nil || len(none) != 0 {
		t.Fatalf("sessions before any = %v, err = %v", none, err)
	}

	if _, err := files.LatestSession("/repo"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("latest before any: err = %v", err)
	}

	older, _ := files.CreateSession("/repo", "older")
	newer, _ := files.CreateSession("/repo", "newer")
	files.CreateSession("/repo/pkg", "inside")
	files.CreateSession("/elsewhere", "other directory")

	if sessions, _ := files.Sessions("/repo"); len(sessions) != 2 || sessions[0].ID != newer.ID {
		t.Fatalf("sessions = %+v, want the one made last first", sessions)
	}

	// Using the older session makes it the latest.
	files.Append(older.ID, "user", Session{})

	latest, err := files.LatestSession("/repo")

	if err != nil || latest.ID != older.ID || latest.Dir != "/repo" {
		t.Fatalf("latest = %+v, err = %v, want the one used last", latest, err)
	}

	sessions, _ := files.Sessions("/repo")

	if len(sessions) != 2 || sessions[0].ID != older.ID || sessions[1].ID != newer.ID {
		t.Fatalf("sessions = %+v", sessions)
	}

	if all, _ := files.Sessions(""); len(all) != 4 {
		t.Fatalf("all sessions = %d, want 4", len(all))
	}

	// Renaming a session is not using it.
	files.RenameSession(newer.ID, "renamed")

	if latest, _ := files.LatestSession("/repo"); latest.ID != older.ID {
		t.Fatal("renaming a session made it the latest")
	}

	// A session being made or deleted when its process died is not listed.
	os.MkdirAll(filepath.Join(files.data, "sessions", ".new-123"), 0o700)

	if all, err := files.Sessions(""); err != nil || len(all) != 4 {
		t.Fatalf("with a session half made: %d sessions, err = %v", len(all), err)
	}
}

func TestSessionsKeepTheirModel(t *testing.T) {
	files := testFiles(t)
	session, _ := files.CreateSession("/project", "test")

	if err := files.SetSessionModel(session.ID, "anthropic", "claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}

	if found, _ := files.Session(session.ID); found.Provider != "anthropic" || found.Model != "claude-opus-5-5" || found.Options != nil {
		t.Fatalf("session = %+v", found)
	}

	if err := files.SetSessionOptions(session.ID, []byte(`{"effort":"high"}`)); err != nil {
		t.Fatal(err)
	}

	if found, _ := files.Session(session.ID); string(found.Options) != `{"effort":"high"}` {
		t.Fatalf("options = %s", found.Options)
	}

	if err := files.SetSessionOptions(session.ID, nil); err != nil {
		t.Fatal(err)
	}

	if found, _ := files.Session(session.ID); found.Options != nil {
		t.Fatalf("options after clearing = %s", found.Options)
	}

	if err := files.RenameSession(session.ID, "a better title"); err != nil {
		t.Fatal(err)
	}

	if found, _ := files.Session(session.ID); found.Title != "a better title" || found.Model != "claude-opus-5-5" {
		t.Fatalf("after renaming, session = %+v", found)
	}

	for _, err := range []error{
		files.SetSessionModel("missing", "anthropic", "x"),
		files.SetSessionOptions("missing", nil),
		files.RenameSession("missing", "x"),
		files.DeleteSession("missing"),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("a session that does not exist: err = %v", err)
		}
	}
}

// A branch starts with a copy of the other session's entries up to a point,
// keeps the rest of what that session was, and leaves it as it is.
func TestBranchesCopyASessionUpToAPoint(t *testing.T) {
	files := testFiles(t)

	source, _ := files.CreateSession("/project", "fix the parser")
	files.SetSessionModel(source.ID, "anthropic", "claude-opus-5-5")
	files.SetSessionOptions(source.ID, json.RawMessage(`{"effort":"high"}`))

	for index := range 5 {
		files.Append(source.ID, "user", map[string]int{"n": index + 1})
	}

	branch, err := files.BranchSession(source.ID, 3)

	if err != nil {
		t.Fatal(err)
	}

	found, _ := files.Session(branch.ID)

	if found.Parent != source.ID || found.ParentSeq != 3 || found.Dir != "/project" ||
		found.Title != "Branch fix the parser" || found.Provider != "anthropic" || string(found.Options) != `{"effort":"high"}` {
		t.Fatalf("branch = %+v", found)
	}

	copied, _ := files.Entries(branch.ID)
	original, _ := files.Entries(source.ID)

	if len(copied) != 3 || len(original) != 5 {
		t.Fatalf("the branch has %d entries and the session %d", len(copied), len(original))
	}

	for index, entry := range copied {
		if entry.Seq != original[index].Seq || string(entry.Payload) != string(original[index].Payload) || !entry.CreatedAt.Equal(original[index].CreatedAt) {
			t.Fatalf("entry %d = %+v, want %+v", index, entry, original[index])
		}
	}

	// A branch of a branch is not called a branch twice.
	if again, _ := files.BranchSession(branch.ID, 3); again.Title != "Branch fix the parser" {
		t.Fatalf("the branch of the branch is called %q", again.Title)
	} else if err := files.DeleteSession(again.ID); err != nil {
		t.Fatal(err)
	}

	// It goes on from there, and is the session -c continues.
	if latest, _ := files.LatestSession("/project"); latest.ID != branch.ID {
		t.Fatalf("the latest session is %s, not the branch", latest.ID)
	}

	if next, _ := files.Append(branch.ID, "user", map[string]int{"n": 9}); next.Seq != 4 {
		t.Fatalf("the branch's next entry is %d", next.Seq)
	}

	// Removing the session it came from leaves the branch whole, and nothing
	// of the session.
	if err := files.DeleteSession(source.ID); err != nil {
		t.Fatal(err)
	}

	if kept, _ := files.Entries(branch.ID); len(kept) != 4 {
		t.Fatalf("the branch kept %d entries", len(kept))
	}

	if gone, err := files.Entries(source.ID); err != nil || len(gone) != 0 {
		t.Fatalf("a deleted session has %d entries, err = %v", len(gone), err)
	}

	if left, _ := os.ReadDir(filepath.Join(files.data, "sessions")); len(left) != 1 || left[0].Name() != branch.ID {
		t.Fatalf("what is left of the sessions: %v", left)
	}

	if _, err := files.BranchSession("missing", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("branching a session that does not exist: err = %v", err)
	}
}

// Recent goes through a directory's sessions from the last entry back, keeps
// the kinds asked for, and gives a payload once, the copies a branch holds
// included.
func TestRecentEntriesComeOnceNewestFirst(t *testing.T) {
	files := testFiles(t)

	first, _ := files.CreateSession("/repo", "first")
	second, _ := files.CreateSession("/repo", "second")
	other, _ := files.CreateSession("/repo/pkg", "another directory")

	for _, appended := range []struct {
		session string
		kind    string
		text    string
	}{
		{first.ID, "user", "fix the parser"},
		{first.ID, "assistant", "done"},
		{second.ID, "user", "write the docs"},
		{second.ID, "steer", "in English"},
		{other.ID, "user", "not this one"},
		{first.ID, "user", "fix the parser"},
	} {
		// Entries are told apart by the millisecond they were appended in.
		time.Sleep(2 * time.Millisecond)
		files.Append(appended.session, appended.kind, map[string]string{"text": appended.text})
	}

	files.BranchSession(second.ID, 2)

	texts := func(limit int) []string {
		entries, err := files.Recent("/repo", []string{"user", "steer"}, limit)

		if err != nil {
			t.Fatal(err)
		}

		var found []string

		for _, entry := range entries {
			var payload map[string]string
			json.Unmarshal(entry.Payload, &payload)
			found = append(found, payload["text"])
		}

		return found
	}

	if got, want := texts(10), []string{"fix the parser", "in English", "write the docs"}; !slices.Equal(got, want) {
		t.Fatalf("recent = %q, want %q", got, want)
	}

	if got := texts(1); !slices.Equal(got, []string{"fix the parser"}) {
		t.Fatalf("recent, one = %q", got)
	}

	if none, err := files.Recent("/repo", nil, 10); err != nil || len(none) != 0 {
		t.Fatalf("no kinds = %v, err = %v", none, err)
	}
}
