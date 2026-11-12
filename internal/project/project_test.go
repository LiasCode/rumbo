package project

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func repository(t *testing.T) string {
	t.Helper()

	// The temporary directory may be reached through a symlink, and git
	// reports the real path.
	dir, _ := filepath.EvalSymlinks(t.TempDir())

	for _, args := range [][]string{{"init", "--quiet"}} {
		command := exec.Command("git", args...)
		command.Dir = dir

		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, output)
		}
	}

	os.MkdirAll(filepath.Join(dir, "pkg", "deep"), 0o755)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "pkg", "deep", "a.go"), []byte("package deep\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("out/\n"), 0o644)

	return dir
}

func TestRootIsTheRepositoryFromAnySubdirectory(t *testing.T) {
	dir := repository(t)
	ctx := context.Background()

	if got := Root(ctx, filepath.Join(dir, "pkg", "deep")); got != dir {
		t.Fatalf("Root from a subdirectory = %q, want %q", got, dir)
	}

	outside := t.TempDir()

	if got := Root(ctx, outside); got != outside {
		t.Fatalf("Root outside a repository = %q, want the directory itself", got)
	}
}

func TestSnapshotChangesOnlyWhenProjectFilesDo(t *testing.T) {
	dir := repository(t)
	ctx := context.Background()
	before := Snapshot(ctx, dir)

	if Snapshot(ctx, dir) != before {
		t.Fatal("two snapshots of an untouched project differ")
	}

	// Ignored output, such as a build directory, is not a change to verify.
	os.MkdirAll(filepath.Join(dir, "out"), 0o755)
	os.WriteFile(filepath.Join(dir, "out", "binary"), []byte("x"), 0o644)

	if Snapshot(ctx, dir) != before {
		t.Fatal("an ignored file changed the snapshot")
	}

	os.WriteFile(filepath.Join(dir, "pkg", "deep", "a.go"), []byte("package deep\n\nvar X = 1\n"), 0o644)
	edited := Snapshot(ctx, dir)

	if edited == before {
		t.Fatal("an edit did not change the snapshot")
	}

	os.WriteFile(filepath.Join(dir, "new.go"), []byte("package main\n"), 0o644)

	if Snapshot(ctx, dir) == edited {
		t.Fatal("a new file did not change the snapshot")
	}

	os.Remove(filepath.Join(dir, "main.go"))

	if len(Files(ctx, dir)) != 3 {
		t.Fatalf("files = %v", Files(ctx, dir))
	}
}
