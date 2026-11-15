package tui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/LiasCode/rumbo/internal/frontends/render"
	"github.com/LiasCode/rumbo/protocol"
)

// A tool call is shown as a block: while it works, a line in the live area
// with a spinner and its time; once it is done, a line with how it went,
// and under it what tells most about it: the change an edit made, or the
// start of what a command printed, and the end of it when it failed.

const (
	markDone    = "✓"
	markFailed  = "✗"
	markUnknown = "·"
	// shownLines bounds the lines a block shows of a result or a change.
	shownLines = 6
)

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spin is the spinner's frame at now.
func spin(now time.Time) string {
	return spinner[now.UnixMilli()/100%int64(len(spinner))]
}

// running is a tool call that has started and not finished.
type running struct {
	name string
	args json.RawMessage
	// started is zero for a call told by a session's history, whose time is
	// not known.
	started time.Time
}

// title names a call by what it acts on, with the name in bold.
func title(name string, args json.RawMessage) string {
	described := render.Tool(name, args)

	return styleBold + name + styleWeightOff + strings.TrimPrefix(described, name)
}

// working is the live line of a call in progress.
func (r *running) working(t theme, now time.Time) string {
	line := t.progress + spin(now) + styleColorOff + " " + title(r.name, r.args)

	if !r.started.IsZero() {
		line += styleDim + "  " + elapsed(now.Sub(r.started)) + styleWeightOff
	}

	return line
}

// exitStatus is how bash says a command failed, on the last line of what
// it returns.
var exitStatus = regexp.MustCompile(`\n?\[([^\[\]\n]+)\]$`)

// finished shows a call once it is done, in rows of at most width columns.
func (r *running) finished(t theme, result string, failed bool, now time.Time, width int) []string {
	mark, status := t.success+markDone, ""
	body := strings.TrimRight(result, "\n")

	if failed {
		mark = t.failure + markFailed

		// What a command printed comes first, and why it failed last.
		if found := exitStatus.FindStringSubmatchIndex(body); found != nil && r.name == "bash" {
			status, body = body[found[2]:found[3]], body[:found[0]]
		}
	}

	head := mark + styleColorOff + " " + title(r.name, r.args)

	if status != "" {
		head += "  " + t.failure + status + styleColorOff
	}

	if !r.started.IsZero() && now.Sub(r.started) >= time.Second {
		head += styleDim + "  " + elapsed(now.Sub(r.started)) + styleWeightOff
	}

	rows := []string{cut(head, width)}

	switch {
	case failed && r.name == "bash":
		rows = append(rows, excerpt(body, width, false, styleDim)...)
	case failed:
		rows = append(rows, excerpt(body, width, true, t.failure)...)
	case r.name == "edit":
		rows = append(rows, change(t, r.args, width)...)
	case r.name == "bash" && body != "[no output]":
		rows = append(rows, excerpt(body, width, true, styleDim)...)
	}

	return rows
}

// unfinished shows a call that is not followed to its end, saying why: it
// never finished, or it goes on in the background.
func (r *running) unfinished(why string, width int) []string {
	return []string{cut(styleDim+markUnknown+" "+title(r.name, r.args)+styleDim+"  ("+why+")", width)}
}

// excerpt shows some lines of text, indented and in style: the first ones,
// or the last ones, which is where a failure says what went wrong.
func excerpt(text string, width int, first bool, style string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}

	lines := strings.Split(text, "\n")
	left := len(lines) - shownLines

	if left > 0 && first {
		lines = lines[:shownLines]
	} else if left > 0 {
		lines = lines[left:]
	}

	var rows []string

	if left > 0 && !first {
		rows = append(rows, styleDim+fmt.Sprintf("    … %d lines before", left))
	}

	for _, line := range lines {
		rows = append(rows, cut(style+"    "+strings.ReplaceAll(line, "\t", "    "), width))
	}

	if left > 0 && first {
		rows = append(rows, styleDim+fmt.Sprintf("    … %d more lines", left))
	}

	return rows
}

// change shows what an edit replaced, as the lines it took away and the
// ones it put in, with those that stayed between them.
func change(t theme, args json.RawMessage, width int) []string {
	var edit struct {
		Old string `json:"old"`
		New string `json:"new"`
	}

	if json.Unmarshal(args, &edit) != nil {
		return nil
	}

	var rows []string

	for _, step := range diff(strings.Split(edit.Old, "\n"), strings.Split(edit.New, "\n")) {
		style := styleDim + "  "

		switch step.op {
		case '-':
			style = t.failure + "- "
		case '+':
			style = t.success + "+ "
		}

		rows = append(rows, cut("    "+style+strings.ReplaceAll(step.text, "\t", "    "), width))
	}

	if len(rows) > 2*shownLines {
		left := len(rows) - 2*shownLines
		rows = append(rows[:2*shownLines], styleDim+fmt.Sprintf("    … %d more lines", left))
	}

	return rows
}

// step is one line of a diff: '-' taken away, '+' put in, or ' ' kept.
type step struct {
	op   byte
	text string
}

// maxDiff bounds the lines a diff is worked out for; past it, every line
// is shown as taken away and put in.
const maxDiff = 400

// diff returns how before became after, line by line, with the fewest
// lines taken away and put in, and the kept lines that are not between
// changes left out.
func diff(before, after []string) []step {
	// The lines both start and end with are kept, and need no comparing.
	head := 0

	for head < len(before) && head < len(after) && before[head] == after[head] {
		head++
	}

	tail := 0

	for tail < len(before)-head && tail < len(after)-head && before[len(before)-1-tail] == after[len(after)-1-tail] {
		tail++
	}

	before, after = before[head:len(before)-tail], after[head:len(after)-tail]

	if len(before) > maxDiff || len(after) > maxDiff {
		var steps []step

		for _, line := range before {
			steps = append(steps, step{'-', line})
		}

		for _, line := range after {
			steps = append(steps, step{'+', line})
		}

		return steps
	}

	// common[i][j] is how many lines before[i:] and after[j:] share, in
	// order.
	common := make([][]int, len(before)+1)

	for i := range common {
		common[i] = make([]int, len(after)+1)
	}

	for i := len(before) - 1; i >= 0; i-- {
		for j := len(after) - 1; j >= 0; j-- {
			if before[i] == after[j] {
				common[i][j] = common[i+1][j+1] + 1
			} else {
				common[i][j] = max(common[i+1][j], common[i][j+1])
			}
		}
	}

	var steps []step

	i, j := 0, 0

	for i < len(before) || j < len(after) {
		switch {
		case i < len(before) && j < len(after) && before[i] == after[j]:
			steps = append(steps, step{' ', before[i]})
			i, j = i+1, j+1
		case j < len(after) && (i == len(before) || common[i][j+1] > common[i+1][j]):
			steps = append(steps, step{'+', after[j]})
			j++
		default:
			steps = append(steps, step{'-', before[i]})
			i++
		}
	}

	return steps
}

// tally is what a run took: its time, when it is known, its turns, and its
// tokens.
type tally struct {
	started time.Time
	turns   int
	spent   protocol.Usage
}

// add counts one turn.
func (t *tally) add(usage *protocol.Usage) {
	t.turns++

	if usage != nil {
		t.spent.Input += usage.Input
		t.spent.Output += usage.Output
		t.spent.CacheRead += usage.CacheRead
		t.spent.CacheWrite += usage.CacheWrite
	}
}

// summary says how a run ended, and what it took.
func (t *tally) summary(colors theme, status, reason string, now time.Time) string {
	took := t.took(now)

	if took != "" {
		took = "  " + styleDim + took + styleReset
	}

	switch status {
	case protocol.RunDone:
		return colors.success + markDone + styleColorOff + styleDim + " done" + styleWeightOff + took
	case protocol.RunCancelled:
		return colors.warning + markFailed + " cancelled" + styleColorOff + took
	}

	if reason == "" {
		reason = status
	}

	return colors.failure + markFailed + " " + reason + styleColorOff + took
}

// took says what a run took: its time, when it is known, its turns, and
// its tokens.
func (t *tally) took(now time.Time) string {
	var parts []string

	if !t.started.IsZero() {
		parts = append(parts, elapsed(now.Sub(t.started)))
	}

	switch t.turns {
	case 0:
	case 1:
		parts = append(parts, "1 turn")
	default:
		parts = append(parts, fmt.Sprintf("%d turns", t.turns))
	}

	if t.turns > 0 {
		parts = append(parts, fmt.Sprintf("%s in, %s cached, %s out", tokens(t.spent.Input), tokens(t.spent.CacheRead), tokens(t.spent.Output)))
	}

	return strings.Join(parts, " · ")
}

// elapsed says a time to the tenth of a second while it is short, and to
// the second after.
func elapsed(took time.Duration) string {
	if took < 10*time.Second {
		return fmt.Sprintf("%.1fs", took.Seconds())
	}

	return took.Round(time.Second).String()
}

// tokens says a count of tokens in thousands or millions past a thousand,
// to a tenth, and without one that is nought.
func tokens(count int64) string {
	switch {
	case count >= 1_000_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(count)/1_000_000), ".0") + "M"
	case count >= 1000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(count)/1000), ".0") + "k"
	}

	return fmt.Sprint(count)
}
