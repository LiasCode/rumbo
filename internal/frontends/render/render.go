// Package render holds the wording the built-in front ends share, so the
// same event reads the same in each of them.
package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LiasCode/rumbo/protocol"
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
	// context and window are the size of the conversation and of the
	// model's context, as of the last response that said.
	context int64
	window  int64
}

// Turn counts one model response, as a turn event reports it.
func (u *Usage) Turn(event protocol.Event) {
	u.Add(event.Usage)

	if event.Context > 0 {
		u.context = event.Context
	}

	if event.Window > 0 {
		u.window = event.Window
	}
}

// Context says how full the model's context is: as a share when its size
// is known, and as a count of tokens otherwise.
func (u *Usage) Context() string {
	switch {
	case u.context == 0:
		return ""
	case u.window > 0:
		return fmt.Sprintf("context %d%% of %dk", u.context*100/u.window, u.window/1000)
	}

	return fmt.Sprintf("context %d tokens", u.context)
}

// Fill returns the size of the conversation and of the model's context, as
// of the last response that said; either is zero while it is not known.
func (u *Usage) Fill() (context, window int64) {
	return u.context, u.window
}

// Total returns the tokens counted.
func (u *Usage) Total() protocol.Usage {
	return u.total
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
