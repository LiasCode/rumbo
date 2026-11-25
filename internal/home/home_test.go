package home

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTheDirectoryIsInTheHomeUnlessRUMBOHOMESaysOtherwise(t *testing.T) {
	t.Setenv("RUMBO_HOME", "")

	user, err := os.UserHomeDir()

	if err != nil {
		t.Fatal(err)
	}

	if dir, err := Dir(); err != nil || dir != filepath.Join(user, ".rumbo") {
		t.Fatalf("Dir = %q, %v", dir, err)
	}

	t.Setenv("RUMBO_HOME", "/elsewhere")

	if dir, err := Dir(); err != nil || dir != "/elsewhere" {
		t.Fatalf("Dir with RUMBO_HOME = %q, %v", dir, err)
	}
}
