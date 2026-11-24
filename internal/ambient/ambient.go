// Package ambient gathers the little the model is told without asking: where
// it is running, the user's standing instructions, and the user's skills.
// Everything else about a task the model finds out with its tools.
package ambient

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/LiasCode/rumbo/internal/transcript"
)

// Collector produces one named section of ambient context for a directory.
// An empty result means the section does not apply.
type Collector interface {
	Name() string
	Collect(ctx context.Context, dir string) string
}

// Fallible is a Collector that may be unable to say its section, such as
// one that asks another program. Try reports whether it could; when it
// could not, the section stays as the model was last told it.
type Fallible interface {
	Collector
	Try(ctx context.Context, dir string) (text string, said bool)
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

// Defaults returns the built-in collectors. skillPlaces are where the
// user's plugins keep skills, besides the user's own and the project's.
func Defaults(skillPlaces ...string) []Collector {
	return []Collector{
		Func("environment", environment),
		Func("instructions", instructions),
		Func("skills", func(_ context.Context, dir string) string { return skills(dir, skillPlaces) }),
	}
}

// Changes runs the collectors and returns the sections that differ from
// known, including removals. Only changes are sent, so an unchanged project
// costs no tokens on later turns. A known section that no collector has any
// more, such as one of an extension the user took out, is removed.
func Changes(ctx context.Context, dir string, collectors []Collector, known map[string]string) []transcript.Section {
	var changed []transcript.Section

	collected := map[string]bool{}

	for _, collector := range collectors {
		collected[collector.Name()] = true

		text, said := "", true

		if fallible, may := collector.(Fallible); may {
			text, said = fallible.Try(ctx, dir)
		} else {
			text = collector.Collect(ctx, dir)
		}

		if !said {
			continue
		}

		if text = strings.TrimSpace(text); text != known[collector.Name()] {
			changed = append(changed, transcript.Section{Name: collector.Name(), Text: text})
		}
	}

	for _, name := range slices.Sorted(maps.Keys(known)) {
		if !collected[name] {
			changed = append(changed, transcript.Section{Name: name})
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

// maxInstructionBytes is how much of one instruction file is sent with every
// request. A longer file is cut, and says where the rest is.
const maxInstructionBytes = 24_000

// bounded cuts instructions that are too long to send whole, at a line, and
// tells the model to read the rest when it needs it.
func bounded(path, text string) string {
	if len(text) <= maxInstructionBytes {
		return text
	}

	kept := text[:maxInstructionBytes]

	if index := strings.LastIndexByte(kept, '\n'); index > 0 {
		kept = kept[:index]
	}

	return fmt.Sprintf("%s\n[These instructions continue: this is the first %d of %d bytes. Read the rest of %s before relying on them.]", kept, len(kept), len(text), path)
}

// instructionFiles are the agent guides a project may carry, in order of
// preference. The first one found in a directory is used.
var instructionFiles = []string{"AGENTS.md", "CLAUDE.md"}

// instructions gathers the user's global guide and the guide in dir, the
// more specific one last. Nothing above dir is read: a session's root is
// the directory it was started in.
func instructions(_ context.Context, dir string) string {
	dirs := []string{dir}

	if user, found := userDir(); found {
		dirs = append([]string{user}, dirs...)
	}

	var parts []string

	for _, candidate := range dirs {
		for _, name := range instructionFiles {
			path := filepath.Join(candidate, name)
			raw, err := os.ReadFile(path)

			if err == nil && strings.TrimSpace(string(raw)) != "" {
				parts = append(parts, fmt.Sprintf("[%s]\n%s", path, bounded(path, strings.TrimSpace(string(raw)))))
				break
			}
		}
	}

	return strings.Join(parts, "\n\n")
}

// userDir is where the user keeps what rumbo reads from them everywhere: the
// global guide and the skills. RUMBO_AMBIENT_DIR overrides only that
// lookup, so evals can isolate it without moving provider credentials.
func userDir() (string, bool) {
	if dir := os.Getenv("RUMBO_AMBIENT_DIR"); dir != "" {
		return dir, true
	}

	config, err := os.UserConfigDir()

	if err != nil {
		return "", false
	}

	return filepath.Join(config, "rumbo"), true
}
