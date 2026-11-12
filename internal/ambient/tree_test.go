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
