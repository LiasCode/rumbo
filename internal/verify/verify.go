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
	checkTimeout = 10 * time.Minute
	// Failure output kept per check; the end is where compilers summarize.
	maxOutput = 8_000
)

// Detect proposes checks for the project in dir from the files it finds.
// Only fast, read-only checks are proposed; test suites are opt-in through the
// checks setting.
func Detect(dir string) []string {
	var checks []string

	// vet also compiles every package, and unlike build it writes no binary
	// into the project.
	if exists(dir, "go.mod") {
		checks = append(checks, "go vet ./...")
	}

	if exists(dir, "Cargo.toml") {
		checks = append(checks, "cargo check --quiet")
	}

	if script := npmScript(dir); script != "" {
		checks = append(checks, "npm run --silent "+script)
	}

	return checks
}

// Result is the outcome of running the checks.
type Result struct {
	Passed bool
	// Report describes the failures, ready to hand to the model.
	Report string
}

// Run executes each check in dir and reports the ones that fail.
func Run(ctx context.Context, dir string, checks []string) Result {
	var failures []string

	for _, check := range checks {
		output, err := tool.Shell(ctx, dir, check, checkTimeout)

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
