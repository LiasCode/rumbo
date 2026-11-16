package transcript

import (
	"testing"

	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/protocol"
)

// A summary counts a session's turns and what they cost, and says how its
// last run ended, whatever came after the ending that is no run.
func TestSummaryTellsHowTheLastRunEnded(t *testing.T) {
	answer := Assistant{Usage: protocol.Usage{Input: 2, CacheRead: 100, CacheWrite: 10, Output: 5}}
	summary := Compaction{Usage: protocol.Usage{Input: 1, Output: 50}}

	run := []store.Entry{
		entry(1, KindSystem, System{Text: "prompt"}),
		entry(2, KindUser, Text{Text: "go"}),
		entry(3, KindAssistant, answer),
		entry(4, KindNote, Note{Type: NoteRetry, Text: "overloaded"}),
		entry(5, KindAssistant, answer),
	}

	for _, check := range []struct {
		name    string
		entries []store.Entry
		ended   string
		runs    int
	}{
		{"a run that works or was killed", run, "", 1},
		{"a failed run", append(run, entry(6, KindNote, Note{Type: NoteFailed, Text: "boom"})), NoteFailed, 1},
		{"a resumed run that works", append(run, entry(6, KindNote, Note{Type: NoteFailed}), entry(7, KindHarness, Text{Text: "resumed"})), "", 1},
		{"a summary asked for after the run", append(run, entry(6, KindNote, Note{Type: NoteDone}), entry(7, KindCompaction, summary)), NoteDone, 1},
		{"a new run", append(run, entry(6, KindNote, Note{Type: NoteCancelled}), entry(7, KindUser, Text{Text: "again"})), "", 2},
	} {
		got := Summarize(check.entries)

		if got.Ended != check.ended || got.Runs != check.runs {
			t.Errorf("%s: ended %q after %d runs, want %q after %d", check.name, got.Ended, got.Runs, check.ended, check.runs)
		}
	}

	got := Summarize(append(run, entry(6, KindCompaction, summary)))
	want := protocol.Usage{Input: 5, CacheRead: 200, CacheWrite: 20, Output: 60}

	if got.Turns != 3 || got.Spent != want {
		t.Fatalf("%d turns spent %+v, want 3 that spent %+v", got.Turns, got.Spent, want)
	}

	if empty := Summarize(nil); empty != (Summary{}) {
		t.Fatalf("a session without entries: %+v", empty)
	}
}
