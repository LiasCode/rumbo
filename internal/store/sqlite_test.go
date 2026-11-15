package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// Two processes are two connections to the same database file.
func TestLockSessionExcludesOtherProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	first, err := Open(path)

	if err != nil {
		t.Fatal(err)
	}

	defer first.Close()

	second, err := Open(path)

	if err != nil {
		t.Fatal(err)
	}

	defer second.Close()

	session, err := first.CreateSession("/project", "/project", "test")

	if err != nil {
		t.Fatal(err)
	}

	release, err := first.LockSession(session.ID)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := second.LockSession(session.ID); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock: err = %v, want ErrLocked", err)
	}

	other, _ := first.CreateSession("/project", "/project", "other")

	if unlock, err := second.LockSession(other.ID); err != nil {
		t.Fatalf("a different session should be free: %v", err)
	} else {
		unlock()
	}

	release()

	if unlock, err := second.LockSession(session.ID); err != nil {
		t.Fatalf("lock after release: %v", err)
	} else {
		unlock()
	}
}

func TestProgramsAreKeptByKind(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))

	if err != nil {
		t.Fatal(err)
	}

	defer database.Close()

	program := Program{Name: "mine", Command: []string{"run", "--fast"}}

	if err := database.PutProgram(KindFrontend, program); err != nil {
		t.Fatal(err)
	}

	if _, err := database.Program(KindProvider, "mine"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a front end was found as a provider: %v", err)
	}

	found, err := database.Program(KindFrontend, "mine")

	if err != nil || len(found.Command) != 2 || string(found.Config) != "{}" {
		t.Fatalf("found = %+v, err = %v", found, err)
	}

	if err := database.DeleteProgram(KindFrontend, "mine"); err != nil {
		t.Fatal(err)
	}

	if err := database.DeleteProgram(KindFrontend, "mine"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting twice: err = %v", err)
	}

	// A provider keeps its options; a front end has none.
	program.Options = json.RawMessage(`{"effort":"high"}`)

	for _, kind := range []string{KindProvider, KindFrontend} {
		if err := database.PutProgram(kind, program); err != nil {
			t.Fatal(err)
		}
	}

	if provider, _ := database.Program(KindProvider, "mine"); string(provider.Options) != `{"effort":"high"}` {
		t.Fatalf("the provider's options = %s", provider.Options)
	}

	if frontend, _ := database.Program(KindFrontend, "mine"); frontend.Options != nil {
		t.Fatalf("the front end's options = %s", frontend.Options)
	}
}

// A database written before sessions had a project is upgraded in place:
// its sessions keep working and belong to their own directory.
func TestOpenUpgradesAnOlderDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	old, err := sql.Open("sqlite", "file:"+path)

	if err != nil {
		t.Fatal(err)
	}

	_, err = old.Exec(`
		CREATE TABLE sessions (id TEXT PRIMARY KEY, dir TEXT NOT NULL, title TEXT NOT NULL, created_at INTEGER NOT NULL);
		CREATE TABLE entries (session_id TEXT NOT NULL, seq INTEGER NOT NULL, kind TEXT NOT NULL, payload TEXT NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY (session_id, seq));
		INSERT INTO sessions VALUES ('abc', '/work/repo', 'old session', 1000);
		INSERT INTO entries VALUES ('abc', 1, 'user', '{"text":"hi"}', 2000);
	`)

	if err != nil {
		t.Fatal(err)
	}

	old.Close()

	for range 2 {
		database, err := Open(path)

		if err != nil {
			t.Fatal(err)
		}

		session, err := database.LatestSession("/work/repo")

		// A session from before providers were stored has none.
		if err != nil || session.ID != "abc" || session.Project != "/work/repo" || session.ActiveAt.UnixMilli() != 2000 || session.Provider != "" {
			t.Fatalf("session = %+v, err = %v", session, err)
		}

		entries, _ := database.Entries("abc")

		if len(entries) != 1 {
			t.Fatalf("entries = %d, want the one that was there", len(entries))
		}

		database.Close()
	}
}

func TestSessionsAreFoundByProjectAndOrderedByUse(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))

	if err != nil {
		t.Fatal(err)
	}

	defer database.Close()

	older, _ := database.CreateSession("/repo/pkg", "/repo", "older")
	newer, _ := database.CreateSession("/repo", "/repo", "newer")
	database.CreateSession("/elsewhere", "/elsewhere", "other project")

	// Using the older session makes it the latest.
	time.Sleep(5 * time.Millisecond)
	database.Append(older.ID, "user", Session{})

	latest, err := database.LatestSession("/repo")

	if err != nil || latest.ID != older.ID || latest.Dir != "/repo/pkg" {
		t.Fatalf("latest = %+v, err = %v, want the one used last", latest, err)
	}

	sessions, _ := database.Sessions("/repo")

	if len(sessions) != 2 || sessions[0].ID != older.ID || sessions[1].ID != newer.ID {
		t.Fatalf("sessions = %+v", sessions)
	}

	if all, _ := database.Sessions(""); len(all) != 3 {
		t.Fatalf("all sessions = %d, want 3", len(all))
	}
}

func TestSessionsKeepTheirModel(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))

	if err != nil {
		t.Fatal(err)
	}

	defer database.Close()

	session, _ := database.CreateSession("/project", "/project", "test")

	if err := database.SetSessionModel(session.ID, "anthropic", "claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}

	if found, _ := database.Session(session.ID); found.Provider != "anthropic" || found.Model != "claude-opus-5-5" || found.Options != nil {
		t.Fatalf("session = %+v", found)
	}

	if err := database.SetSessionOptions(session.ID, []byte(`{"effort":"high"}`)); err != nil {
		t.Fatal(err)
	}

	if found, _ := database.Session(session.ID); string(found.Options) != `{"effort":"high"}` {
		t.Fatalf("options = %s", found.Options)
	}

	if err := database.SetSessionOptions(session.ID, nil); err != nil {
		t.Fatal(err)
	}

	if found, _ := database.Session(session.ID); found.Options != nil {
		t.Fatalf("options after clearing = %s", found.Options)
	}

	for _, err := range []error{
		database.SetSessionModel("missing", "anthropic", "x"),
		database.SetSessionOptions("missing", nil),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("a session that does not exist: err = %v", err)
		}
	}
}

// A branch starts with a copy of the other session's entries up to a point,
// keeps the rest of what that session was, and leaves it as it is.
func TestBranchesCopyASessionUpToAPoint(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))

	if err != nil {
		t.Fatal(err)
	}

	defer database.Close()

	source, _ := database.CreateSession("/project/sub", "/project", "fix the parser")
	database.SetSessionModel(source.ID, "anthropic", "claude-opus-5-5")
	database.SetSessionOptions(source.ID, json.RawMessage(`{"effort":"high"}`))

	for index := range 5 {
		database.Append(source.ID, "user", map[string]int{"n": index + 1})
	}

	branch, err := database.BranchSession(source.ID, 3)

	if err != nil {
		t.Fatal(err)
	}

	found, _ := database.Session(branch.ID)

	if found.Parent != source.ID || found.ParentSeq != 3 || found.Dir != "/project/sub" || found.Project != "/project" ||
		found.Title != "fix the parser" || found.Provider != "anthropic" || string(found.Options) != `{"effort":"high"}` {
		t.Fatalf("branch = %+v", found)
	}

	copied, _ := database.Entries(branch.ID)
	original, _ := database.Entries(source.ID)

	if len(copied) != 3 || len(original) != 5 {
		t.Fatalf("the branch has %d entries and the session %d", len(copied), len(original))
	}

	for index, entry := range copied {
		if entry.Seq != original[index].Seq || string(entry.Payload) != string(original[index].Payload) || !entry.CreatedAt.Equal(original[index].CreatedAt) {
			t.Fatalf("entry %d = %+v, want %+v", index, entry, original[index])
		}
	}

	// It goes on from there, and is the session -c continues.
	if next, _ := database.Append(branch.ID, "user", map[string]int{"n": 9}); next.Seq != 4 {
		t.Fatalf("the branch's next entry is %d", next.Seq)
	}

	if latest, _ := database.LatestSession("/project"); latest.ID != branch.ID {
		t.Fatalf("the latest session is %s, not the branch", latest.ID)
	}

	// Removing the session it came from leaves the branch whole.
	database.DeleteSession(source.ID)

	if kept, _ := database.Entries(branch.ID); len(kept) != 4 {
		t.Fatalf("the branch kept %d entries", len(kept))
	}

	if _, err := database.BranchSession("missing", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("branching a session that does not exist: err = %v", err)
	}
}

// Recent goes through a project's sessions from the last entry back, keeps
// the kinds asked for, and gives a payload once, the copies a branch holds
// included.
func TestRecentEntriesComeOnceNewestFirst(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))

	if err != nil {
		t.Fatal(err)
	}

	defer database.Close()

	first, _ := database.CreateSession("/repo", "/repo", "first")
	second, _ := database.CreateSession("/repo/pkg", "/repo", "second")
	other, _ := database.CreateSession("/elsewhere", "/elsewhere", "other project")

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
		database.Append(appended.session, appended.kind, map[string]string{"text": appended.text})
	}

	database.BranchSession(second.ID, 2)

	texts := func(limit int) []string {
		entries, err := database.Recent("/repo", []string{"user", "steer"}, limit)

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

	if none, err := database.Recent("/repo", nil, 10); err != nil || len(none) != 0 {
		t.Fatalf("no kinds = %v, err = %v", none, err)
	}
}
