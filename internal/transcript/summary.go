package transcript

import (
	"encoding/json"

	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/protocol"
)

// Summary is what a session's entries add up to for a person: how much it
// worked, what the providers counted for it, and how its last run ended.
type Summary struct {
	// Turns are the responses the session holds: the model's answers, and
	// the summaries that replaced a conversation. Each answered a request.
	Turns int
	// Spent adds up what the providers counted for those requests.
	Spent protocol.Usage
	// Ended is how the last run ended: done, failed, or cancelled. It is
	// empty when the session has no run, and when its last run has no
	// ending, because it was killed or still works.
	Ended string
	// Runs counts the objectives the session was given.
	Runs int
}

// IsResponse reports whether an entry of kind answered a request.
func IsResponse(kind string) bool {
	return kind == KindAssistant || kind == KindCompaction
}

// Summarize adds up a session's entries.
func Summarize(entries []store.Entry) Summary {
	var summary Summary

	for _, entry := range entries {
		switch {
		case entry.Kind == KindUser:
			summary.Runs++
			summary.Ended = ""

		case entry.Kind == KindHarness:
			// After an ending, the harness speaks only to resume the run.
			summary.Ended = ""

		case entry.Kind == KindNote:
			var note Note

			if json.Unmarshal(entry.Payload, &note) == nil && ends(note.Type) {
				summary.Ended = note.Type
			}

		case IsResponse(entry.Kind):
			var answer struct {
				Usage protocol.Usage `json:"usage"`
			}

			summary.Turns++

			if json.Unmarshal(entry.Payload, &answer) == nil {
				summary.Spent.Input += answer.Usage.Input
				summary.Spent.CacheRead += answer.Usage.CacheRead
				summary.Spent.CacheWrite += answer.Usage.CacheWrite
				summary.Spent.Output += answer.Usage.Output
			}
		}
	}

	return summary
}

// ends reports whether a note of kind closes a run.
func ends(kind string) bool {
	return kind == NoteDone || kind == NoteFailed || kind == NoteCancelled
}
