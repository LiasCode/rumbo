package verify

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDetectUsesTheNearestDirectoryThatDefinesChecks(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "services", "api", "internal")
	os.MkdirAll(deep, 0o755)
	os.WriteFile(filepath.Join(root, "Makefile"), []byte("build:\n\ttrue\n\ncheck: build\n\ttrue\n"), 0o644)
	os.WriteFile(filepath.Join(root, "services", "api", "go.mod"), []byte("module api\n"), 0o644)

	checks, where := Detect(deep, root)

	if !reflect.DeepEqual(checks, []string{"go vet ./..."}) || where != filepath.Join(root, "services", "api") {
		t.Fatalf("from inside the module: %v in %s", checks, where)
	}

	checks, where = Detect(filepath.Join(root, "services"), root)

	if !reflect.DeepEqual(checks, []string{"make check"}) || where != root {
		t.Fatalf("from outside the module: %v in %s", checks, where)
	}

	// The search stops at the project root, whatever lies above it.
	empty := filepath.Join(t.TempDir(), "project")
	os.MkdirAll(filepath.Join(empty, "src"), 0o755)

	if checks, where := Detect(filepath.Join(empty, "src"), empty); checks != nil || where != filepath.Join(empty, "src") {
		t.Fatalf("with nothing to detect: %v in %s", checks, where)
	}
}

func TestDetectPrefersAPackageScriptOverTheBareCompiler(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte("{}"), 0o644)

	if checks, _ := Detect(dir, dir); !reflect.DeepEqual(checks, []string{"npx --no-install tsc --noEmit"}) {
		t.Fatalf("checks = %v", checks)
	}

	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"typecheck":"tsc --noEmit","test":"vitest"}}`), 0o644)

	if checks, _ := Detect(dir, dir); !reflect.DeepEqual(checks, []string{"npm run --silent typecheck"}) {
		t.Fatalf("checks = %v", checks)
	}
}

func TestRunReportsFailuresAndTimeouts(t *testing.T) {
	dir := t.TempDir()

	result := Run(context.Background(), dir, []string{"true", "echo broken here; exit 2", "sleep 5"}, 300*time.Millisecond)

	if result.Passed || !strings.Contains(result.Report, "$ echo broken here; exit 2\nbroken here\n[exit status 2]") ||
		!strings.Contains(result.Report, "$ sleep 5") || !strings.Contains(result.Report, "timed out") {
		t.Fatalf("report = %q", result.Report)
	}

	if strings.Contains(result.Report, "$ true") {
		t.Fatal("a passing check is in the report")
	}

	if result := Run(context.Background(), dir, []string{"true"}, 0); !result.Passed {
		t.Fatalf("with no timeout given the default applies: %q", result.Report)
	}
}
