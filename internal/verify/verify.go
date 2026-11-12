// Package verify runs the project's checks when the agent says it is done.
// A failing check sends the agent back to work, so "done" means the project
// builds, not that the model stopped talking.
package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rumbo/internal/tool"
)

const (
	// Failure output kept per check; the end is where compilers summarize.
	maxOutput = 8_000
	// DefaultTimeout is what a check gets when none is given.
	DefaultTimeout = 10 * time.Minute
)

// Detect proposes checks for work done in dir, and the directory to run them
// in. It looks in dir and then in each directory above it, up to root, and
// uses the first one that says how the project is built: the checks of a
// module apply to work anywhere inside it.
//
// Only fast checks that change nothing are proposed. Test suites are opt-in
// through the checks setting, because they can be slow and may already fail.
func Detect(dir, root string) (checks []string, where string) {
	for current := dir; ; current = filepath.Dir(current) {
		if checks := detectIn(current); len(checks) > 0 {
			return checks, current
		}

		if current == root || current == filepath.Dir(current) {
			return nil, dir
		}
	}
}

func detectIn(dir string) []string {
	var checks []string

	// vet also compiles every package, and unlike build it writes no binary
	// into the project.
	if exists(dir, "go.mod") {
		checks = append(checks, "go vet ./...")
	}

	if exists(dir, "Cargo.toml") {
		checks = append(checks, "cargo check --quiet")
	}

	switch script := npmScript(dir); {
	case script != "":
		checks = append(checks, "npm run --silent "+script)
	case exists(dir, "tsconfig.json"):
		// Without --no-install, npx would download a compiler on its own.
		checks = append(checks, "npx --no-install tsc --noEmit")
	}

	if makeTarget(dir, "check") {
		checks = append(checks, "make check")
	}

	return checks
}

// Result is the outcome of running the checks.
type Result struct {
	Passed bool
	// Report describes the failures, ready to hand to the model.
	Report string
}

// Run executes each check in dir, giving each one timeout, and reports the
// ones that fail.
func Run(ctx context.Context, dir string, checks []string, timeout time.Duration) Result {
	var failures []string

	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	for _, check := range checks {
		output, err := tool.Shell(ctx, dir, check, timeout)

		if err == nil {
			continue
		}

		output = strings.TrimSpace(output)

		if len(output) > maxOutput {
			output = "[...]\n" + output[len(output)-maxOutput:]
		}

		failures = append(failures, fmt.Sprintf("$ %s\n%s\n[%s]", check, output, err))
	}

	return Result{Passed: len(failures) == 0, Report: strings.Join(failures, "\n\n")}
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))

	return err == nil
}

// makeTarget reports whether the Makefile in dir defines the target.
func makeTarget(dir, target string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "Makefile"))

	if err != nil {
		return false
	}

	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, target+":") {
			return true
		}
	}

	return false
}

// npmScript returns the first conventional check script the package defines.
func npmScript(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))

	if err != nil {
		return ""
	}

	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}

	if json.Unmarshal(raw, &manifest) != nil {
		return ""
	}

	for _, name := range []string{"check", "typecheck", "lint"} {
		if _, ok := manifest.Scripts[name]; ok {
			return name
		}
	}

	return ""
}
