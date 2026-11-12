// Package render holds the wording the built-in front ends share, so the
// same event reads the same in each of them.
package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"rumbo/protocol"
)

// Tool describes a tool call by the argument that identifies it: the command
// or the path.
func Tool(name string, args json.RawMessage) string {
	var fields map[string]any

	if json.Unmarshal(args, &fields) == nil {
		for _, key := range []string{"command", "path"} {
			if value, ok := fields[key].(string); ok {
				return name + " " + FirstLine(value)
			}
		}
	}

	return name
}

// FirstLine returns the first line of text, shortened to fit a status line.
func FirstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")

	if runes := []rune(line); len(runes) > 160 {
		return string(runes[:160]) + "..."
	}

	return line
}

// Usage totals the tokens of a run.
type Usage struct {
	total protocol.Usage
}

// Add counts one model response.
func (u *Usage) Add(usage *protocol.Usage) {
	if usage == nil {
		return
	}

	u.total.Input += usage.Input
	u.total.Output += usage.Output
	u.total.CacheRead += usage.CacheRead
	u.total.CacheWrite += usage.CacheWrite
}

func (u *Usage) String() string {
	return fmt.Sprintf("%d in, %d cached, %d out", u.total.Input, u.total.CacheRead, u.total.Output)
}
