// Package tool holds the tools the model can call. The set is deliberately
// small: read, write, edit, and bash cover programming work, and everything
// else is reachable through bash.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"rumbo/protocol"
)

// Tool is one capability offered to the model.
type Tool interface {
	Def() protocol.ToolDef
	// Mutates reports whether a call may change the project, which is what
	// makes the harness verify the work when the agent stops.
	Mutates() bool
	// Run executes the call in dir. A returned error is shown to the model as
	// a failed result, so its message should say how to recover.
	Run(ctx context.Context, dir string, args json.RawMessage) (string, error)
}

// Defaults returns the built-in tools.
func Defaults() []Tool {
	return []Tool{Read{}, Write{}, Edit{}, Bash{}}
}

// Output limits. Results beyond them are cut, and the cut says how to get the
// rest, so one large file or log cannot flood the context.
const (
	maxBytes = 40_000
	maxLines = 1_500
)

func decode(args json.RawMessage, into any) error {
	if err := json.Unmarshal(args, into); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}

	return nil
}

func resolve(dir, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}

	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}

	return filepath.Join(dir, path), nil
}

// tail keeps the end of text within the output limits, because the end of a
// command's output is where errors and results are.
func tail(text string) string {
	lines := strings.Split(text, "\n")
	total := len(lines)
	size := 0
	start := total

	for start > 0 && total-start < maxLines && size+len(lines[start-1])+1 <= maxBytes {
		start--
		size += len(lines[start]) + 1
	}

	if start == 0 {
		return text
	}

	// A single line larger than the limit is cut by bytes instead.
	if start == total {
		return fmt.Sprintf("[output truncated: last %d bytes of %d]\n%s", maxBytes, len(text), text[len(text)-maxBytes:])
	}

	return fmt.Sprintf("[output truncated: last %d of %d lines]\n%s", total-start, total, strings.Join(lines[start:], "\n"))
}

func schema(properties string, required ...string) json.RawMessage {
	quoted, _ := json.Marshal(required)

	return json.RawMessage(fmt.Sprintf(
		`{"type":"object","properties":%s,"required":%s,"additionalProperties":false}`,
		properties, quoted,
	))
}
