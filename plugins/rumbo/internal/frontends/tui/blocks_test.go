package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LiasCode/rumbo/protocol"
)

// shownDiff is a diff as its lines read.
func shownDiff(steps []step) string {
	var lines []string

	for _, each := range steps {
		lines = append(lines, string(each.op)+each.text)
	}

	return strings.Join(lines, "|")
}

// A diff takes away and puts in as few lines as it can, and keeps the
// lines between changes but not those around them.
func TestADiffShowsWhatChanged(t *testing.T) {
	for _, test := range []struct {
		before, after string
		want          string
	}{
		{"a\nb\nc", "a\nB\nc", "-b|+B"},
		{"a\nb\nc\nd\ne", "a\nB\nc\nD\ne", "-b|+B| c|-d|+D"},
		{"keep\nold", "keep\nold\nnew", "+new"},
		{"one", "", "-one|+"},
	} {
		if got := shownDiff(diff(strings.Split(test.before, "\n"), strings.Split(test.after, "\n"))); got != test.want {
			t.Errorf("diff(%q, %q) = %q, want %q", test.before, test.after, got, test.want)
		}
	}
}

// A finished call says how it went; a command shows the start of what it
// printed, or the end of it and its status when it failed, and an edit the
// change it made.
func TestAFinishedCallShowsWhatTellsMost(t *testing.T) {
	started := time.Now().Add(-2500 * time.Millisecond)
	command := &running{name: "bash", args: json.RawMessage(`{"command":"go test ./..."}`), started: started}

	rows := command.finished(defaultTheme, "FAIL x\nok y\n[exit status 1]", true, time.Now(), 80)

	if head := plain(rows[0]); head != "✗ bash go test ./...  exit status 1  2.5s" {
		t.Fatalf("head = %q", head)
	}

	if len(rows) != 3 || plain(rows[1]) != "    FAIL x" || plain(rows[2]) != "    ok y" {
		t.Fatalf("rows = %q", rows)
	}

	long := strings.Repeat("line\n", 20)

	if rows := command.finished(defaultTheme, long, false, time.Now(), 80); len(rows) != 1+shownLines+1 || !strings.Contains(rows[len(rows)-1], "14 more lines") {
		t.Fatalf("a long output: %q", rows)
	}

	if rows := command.finished(defaultTheme, "[no output]", false, time.Now(), 80); len(rows) != 1 || !strings.HasPrefix(plain(rows[0]), "✓ bash") {
		t.Fatalf("no output: %q", rows)
	}

	edit := &running{name: "edit", args: json.RawMessage(`{"path":"a.go","old":"x := 1\ny := 2","new":"x := 1\ny := 3"}`)}
	rows = edit.finished(defaultTheme, "edited a.go (1 replacement(s))", false, time.Now(), 80)

	if len(rows) != 3 || plain(rows[0]) != "✓ edit a.go" || plain(rows[1]) != "    - y := 2" || plain(rows[2]) != "    + y := 3" {
		t.Fatalf("edit: %q", rows)
	}

	// A call told by a history has no time.
	if rows := edit.finished(defaultTheme, "old was not found in a.go", true, time.Now(), 80); plain(rows[0]) != "✗ edit a.go" || plain(rows[1]) != "    old was not found in a.go" {
		t.Fatalf("a failed edit: %q", rows)
	}
}

// The end of a run says how it ended, how long it took when that is known,
// its turns, and its tokens. What the provider wrote to its cache was new
// input too.
func TestARunEndsWithWhatItTook(t *testing.T) {
	run := tally{started: time.Now().Add(-75 * time.Second)}
	run.add(&protocol.Usage{Input: 12345, CacheRead: 10000, Output: 999})
	run.add(&protocol.Usage{Input: 100, CacheWrite: 2000, Output: 1})

	if got := plain(run.summary(defaultTheme, protocol.RunDone, "", time.Now())); got != "✓ done  1m15s · 2 turns · 14.4k in, 10k cached, 1k out" {
		t.Fatalf("done = %q", got)
	}

	told := tally{}

	if got := plain(told.summary(defaultTheme, protocol.RunFailed, "the provider went away", time.Now())); got != "✗ the provider went away" {
		t.Fatalf("failed = %q", got)
	}

	if elapsed(400*time.Millisecond) != "0.4s" || tokens(2_500_000) != "2.5M" {
		t.Fatalf("elapsed or tokens")
	}
}
