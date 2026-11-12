package store

import (
	"errors"
	"path/filepath"
	"testing"
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

	session, err := first.CreateSession("/project", "test")

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

	other, _ := first.CreateSession("/project", "other")

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
