package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestALockIsHeldUntilItsFileCloses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "held")

	if err := os.WriteFile(path, []byte("written"), 0o600); err != nil {
		t.Fatal(err)
	}

	first, err := os.OpenFile(path, os.O_RDWR, 0)

	if err != nil {
		t.Fatal(err)
	}

	if err := Lock(first); err != nil {
		t.Fatal(err)
	}

	second, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0)

	if err != nil {
		t.Fatal(err)
	}

	defer second.Close()

	if err := TryLock(second); !errors.Is(err, ErrHeld) {
		t.Fatalf("TryLock while held = %v", err)
	}

	// What the file holds can still be written while it is locked.
	if _, err := second.WriteString(" more"); err != nil {
		t.Fatalf("writing a locked file: %v", err)
	}

	first.Close()

	if err := TryLock(second); err != nil {
		t.Fatalf("TryLock once released = %v", err)
	}
}

func TestHoldRunsWhatItIsGiven(t *testing.T) {
	ran := false

	if err := Hold(t.TempDir(), func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("Hold = %v, ran = %v", err, ran)
	}
}
