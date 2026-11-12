package store

import (
	"database/sql"
	"errors"
	"path/filepath"
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

		if err != nil || session.ID != "abc" || session.Project != "/work/repo" || session.ActiveAt.UnixMilli() != 2000 {
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
