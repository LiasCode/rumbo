package ambient

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTreeCollapsesToBudget(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), nil, 0o644)

	for index := range 400 {
		path := filepath.Join(dir, "pkg", fmt.Sprintf("sub%d", index%4), fmt.Sprintf("file%d.go", index))
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, nil, 0o644)
	}

	os.MkdirAll(filepath.Join(dir, "node_modules", "x"), 0o755)
	os.WriteFile(filepath.Join(dir, "node_modules", "x", "index.js"), nil, 0o644)

	got := tree(context.Background(), dir)
	want := "pkg/\n  sub0/ (100 files)\n  sub1/ (100 files)\n  sub2/ (100 files)\n  sub3/ (100 files)\nmain.go"

	if got != want {
		t.Fatalf("tree =\n%s\nwant\n%s", got, want)
	}

	if strings.Contains(got, "node_modules") {
		t.Fatal("ignored directory listed")
	}
}

func TestTreeSaysHowLargeTheLargeFilesAre(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "small.go"), []byte("package x\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "settings.ini"), []byte(strings.Repeat("option = 1234\n", 3120)), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.md"), []byte(strings.Repeat("a line of ordinary prose\n", 840)), 0o644)
	os.WriteFile(filepath.Join(dir, "logo.png"), append([]byte{0x89, 'P', 'N', 'G', 0}, make([]byte, 40_000)...), 0o644)

	got := tree(context.Background(), dir)
	want := "logo.png (40 KB)\nnotes.md (~800 lines)\nsettings.ini (~3.1k lines)\nsmall.go"

	if got != want {
		t.Fatalf("tree =\n%s\nwant\n%s", got, want)
	}
}
