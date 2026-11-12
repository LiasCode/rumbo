// Package ambient gathers the context every programming task needs before
// the model asks for it: the directory tree, the git state, the project's
// instructions, and the environment. The harness collects it with ordinary
// tools, so the model spends no turns or tokens fetching it.
package ambient

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"rumbo/internal/project"
	"rumbo/internal/tool"
	"rumbo/internal/transcript"
)

// Collector produces one named section of ambient context for a directory.
// An empty result means the section does not apply.
type Collector interface {
	Name() string
	Collect(ctx context.Context, dir string) string
}

// Func adapts a function to a Collector.
func Func(name string, collect func(ctx context.Context, dir string) string) Collector {
	return funcCollector{name: name, collect: collect}
}

type funcCollector struct {
	name    string
	collect func(ctx context.Context, dir string) string
}

func (f funcCollector) Name() string {
	return f.name
}

func (f funcCollector) Collect(ctx context.Context, dir string) string {
	return f.collect(ctx, dir)
}

// Defaults returns the built-in collectors, most stable first so the sections
// that change often sit at the end.
func Defaults() []Collector {
	return []Collector{
		Func("environment", environment),
		Func("instructions", instructions),
		Func("tree", tree),
		Func("git", git),
	}
}

// Changes runs the collectors and returns the sections that differ from
// known, including removals. Only changes are sent, so an unchanged project
// costs no tokens on later turns.
func Changes(ctx context.Context, dir string, collectors []Collector, known map[string]string) []transcript.Section {
	var changed []transcript.Section

	for _, collector := range collectors {
		text := strings.TrimSpace(collector.Collect(ctx, dir))

		if text != known[collector.Name()] {
			changed = append(changed, transcript.Section{Name: collector.Name(), Text: text})
		}
	}

	return changed
}

func environment(_ context.Context, dir string) string {
	return fmt.Sprintf(
		"working directory: %s\nplatform: %s/%s\ndate: %s",
		dir, runtime.GOOS, runtime.GOARCH, time.Now().Format("2006-01-02"),
	)
}

// instructionFiles are the agent guides a project may carry, in order of
// preference. The first one found in a directory is used.
var instructionFiles = []string{"AGENTS.md", "CLAUDE.md"}

// instructions gathers the user's global guide and the guides from the
// repository root down to dir, so the most specific one comes last.
func instructions(ctx context.Context, dir string) string {
	var dirs []string

	root := project.Root(ctx, dir)

	for current := dir; ; current = filepath.Dir(current) {
		dirs = append([]string{current}, dirs...)

		if current == root || current == filepath.Dir(current) {
			break
		}
	}

	if config, err := os.UserConfigDir(); err == nil {
		dirs = append([]string{filepath.Join(config, "rumbo")}, dirs...)
	}

	var parts []string

	for _, candidate := range dirs {
		for _, name := range instructionFiles {
			path := filepath.Join(candidate, name)
			raw, err := os.ReadFile(path)

			if err == nil && strings.TrimSpace(string(raw)) != "" {
				parts = append(parts, fmt.Sprintf("[%s]\n%s", path, strings.TrimSpace(string(raw))))
				break
			}
		}
	}

	return strings.Join(parts, "\n\n")
}

func git(ctx context.Context, dir string) string {
	status := run(ctx, dir, "git status --short --branch 2>/dev/null | head -n 40")

	if strings.TrimSpace(status) == "" {
		return ""
	}

	log := run(ctx, dir, "git log --oneline -n 5 2>/dev/null")

	if strings.TrimSpace(log) == "" {
		return "status:\n" + status
	}

	return "status:\n" + status + "\nrecent commits:\n" + log
}

func run(ctx context.Context, dir, command string) string {
	output, _ := tool.Shell(ctx, dir, command, 10*time.Second)

	return output
}
