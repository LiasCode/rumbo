package tui

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/LiasCode/rumbo/internal/frontends/render"
	"github.com/LiasCode/rumbo/protocol"
)

// conversation is what the terminal UI shows of one session: what the
// person asked and told its runs, what the model wrote, the tools it
// called, and what the host, the provider, and commands said. It is built
// from the protocol's events alone, and kept while a rumbo is held for the
// session, so a session shown again is as it was left.
type conversation struct {
	session string
	entries []*entry
	// loaded is true once the session's history is in it, and loading
	// while it is asked for; a new session's has none to wait for.
	loaded  bool
	loading bool
	// usage is what the session's runs spent, and run what the run told
	// last took so far.
	usage render.Usage
	run   tally
	// reply is the model's last answer, what it wrote since the last thing
	// that was not text, which /copy takes; replying is true while it goes
	// on.
	reply    string
	replying bool
	// top is the first row shown, unless follow keeps the last ones in view.
	top    int
	follow bool
	// selected is the entry chosen while the conversation has the keys, -1
	// for none, and spans where each entry was drawn last, by row.
	selected int
	spans    []span
	// label marks what the host or the provider said.
	label func(source string) string
}

// span is where an entry is drawn: from its first row to before its last.
type span struct {
	entry    int
	from, to int
}

// The kinds of entry, by what the person sees them as.
const (
	entryTask = iota
	entrySteer
	entryReply
	entryTool
	entryContext
	entryLog
	entryEnded
	// entryTyped is what the person typed that is not for the model: a
	// command, an answer to a question, or a confirmation.
	entryTyped
	// entryAnswer is what a command answered, or why it failed, and
	// entrySaid what a provider's function said or asked while it worked.
	entryAnswer
	entrySaid
)

// entry is one thing the conversation shows.
type entry struct {
	kind   int
	text   string
	images []protocol.Image
	failed bool
	// label marks a log line or an answer with where it comes from.
	label string
	// call is a tool call, and result what it returned once finished; ended
	// is when it finished, or when its run ended for one that never did,
	// and why says why it is not followed further.
	call     running
	result   string
	finished bool
	why      string
	ended    time.Time
	// status, with text, is how a run ended, and took what it took.
	status   string
	took     tally
	sections []string
	// open is true while the model still writes the reply.
	open bool
	// expanded is true for a call shown with all it was given and all it
	// returned, and resumed for what stands for a run resumed, which is no
	// task the person gave.
	expanded bool
	resumed  bool
	// rows are the entry drawn width columns wide, until it changes.
	rows  []string
	width int
}

func newConversation(session string, label func(string) string) *conversation {
	return &conversation{session: session, follow: true, selected: -1, label: label}
}

// add puts an entry at the end, and ends the reply that was being written.
func (c *conversation) add(added *entry) {
	c.close()
	c.entries = append(c.entries, added)
}

// close ends the reply being written, if one is.
func (c *conversation) close() {
	if last := c.last(); last != nil && last.open {
		last.open, last.rows = false, nil
	}
}

func (c *conversation) last() *entry {
	if len(c.entries) == 0 {
		return nil
	}

	return c.entries[len(c.entries)-1]
}

// ask shows what the person asked a run started at now, which starts what
// the run takes. A run resumed shows what stands for it, which is no task.
func (c *conversation) ask(text string, images []protocol.Image, resumed bool, now time.Time) {
	c.settle("it never finished", now)
	c.run = tally{started: now}
	c.add(&entry{kind: entryTask, text: text, images: images, resumed: resumed})
}

// point is the number of the person's message an entry is, counted from 1
// among their tasks, as the host numbers the points a branch starts
// before, or 0 for an entry that is no task.
func (c *conversation) point(index int) int {
	if index < 0 || index >= len(c.entries) || c.entries[index].kind != entryTask || c.entries[index].resumed {
		return 0
	}

	number := 0

	for _, each := range c.entries[:index+1] {
		if each.kind == entryTask && !each.resumed {
			number++
		}
	}

	return number
}

// selectable reports whether an entry can be chosen, to act on it.
func selectable(kind int) bool {
	return kind != entryContext && kind != entryEnded && kind != entryTyped
}

// choose moves the selection step entries among those that can be chosen,
// stopping at either end; with none selected, it starts from the last.
func (c *conversation) choose(step int) {
	var chosen []int

	for index, each := range c.entries {
		if selectable(each.kind) {
			chosen = append(chosen, index)
		}
	}

	if len(chosen) == 0 {
		c.selected = -1
		return
	}

	at := slices.Index(chosen, c.selected)

	switch {
	case at < 0:
		at = len(chosen) - 1
	default:
		at = max(min(at+step, len(chosen)-1), 0)
	}

	c.selected = chosen[at]
}

// at returns the entry drawn on a row, as rows drew it last.
func (c *conversation) at(row int) (int, bool) {
	for _, drawn := range c.spans {
		if row >= drawn.from && row < drawn.to {
			return drawn.entry, true
		}
	}

	return 0, false
}

// span of an entry, as rows drew it last.
func (c *conversation) spanOf(index int) (span, bool) {
	for _, drawn := range c.spans {
		if drawn.entry == index {
			return drawn, true
		}
	}

	return span{}, false
}

// apply shows one event of a run, followed as it works at now or told by a
// session's history, which also tells what each objective was and how
// each run ended.
func (c *conversation) apply(event protocol.Event, told bool, now time.Time) {
	if event.Type != protocol.EventText && event.Type != protocol.EventTurn {
		c.replying = false
	}

	switch event.Type {
	case protocol.EventText:
		if !c.replying {
			c.reply, c.replying = "", true
		}

		c.reply += event.Text

		if last := c.last(); last != nil && last.kind == entryReply && last.open {
			last.text += event.Text
			last.rows = nil

			return
		}

		c.add(&entry{kind: entryReply, text: event.Text, open: true})

	case protocol.EventContext:
		c.add(&entry{kind: entryContext, sections: event.Sections})

	case protocol.EventToolStarted:
		called := running{name: event.Name, args: event.Args}

		if !told {
			called.started = now
		}

		c.add(&entry{kind: entryTool, call: called})

	case protocol.EventToolProgress:
		// What a call says it is doing shows on its line while it works.
		if index := c.workingOn(event.Name); index >= 0 {
			c.entries[index].call.doing = render.FirstLine(strings.TrimSpace(event.Text))
		}

	case protocol.EventToolFinished:
		c.finish(event, now)

	case protocol.EventTurn:
		c.run.add(event.Usage)
		c.usage.Turn(event)

	case protocol.EventObjective:
		c.settle("it never finished", now)
		c.run = tally{}
		c.add(&entry{kind: entryTask, text: event.Text, images: event.Images})

	case protocol.EventSteer:
		c.add(&entry{kind: entrySteer, text: event.Text, images: event.Images})

	case protocol.EventEnded:
		c.end(event.Status, event.Text, now)

	case protocol.EventLog:
		// A log line is the provider's own output, an extension's, or
		// something the host did; which one is said, since they call for
		// different fixes.
		source := fromHost

		switch event.Source {
		case protocol.TargetProvider:
			source = fromProvider
		case protocol.TargetExtension:
			source = fromExtensions
		}

		c.add(&entry{kind: entryLog, text: event.Text, label: c.label(source)})
	}
}

// finish gives its result to the oldest call of the tool still working:
// the events name the tool and not the call, and calls finish in the order
// they started. A call that started while nothing followed it is known by
// its name alone.
func (c *conversation) finish(event protocol.Event, now time.Time) {
	index := c.workingOn(event.Name)

	if index < 0 {
		c.add(&entry{kind: entryTool, call: running{name: event.Name}})
		index = len(c.entries) - 1
	}

	done := c.entries[index]
	done.result, done.failed, done.finished, done.ended = event.Result, event.Failed, true, now
	done.rows = nil
}

// workingOn returns the oldest call of the tool called name still working,
// or -1.
func (c *conversation) workingOn(name string) int {
	return slices.IndexFunc(c.entries, func(each *entry) bool {
		return each.kind == entryTool && each.working() && each.call.name == name
	})
}

// working reports whether an entry is a call that may still finish.
func (e *entry) working() bool {
	return e.kind == entryTool && !e.finished && e.why == ""
}

// settle shows the calls still working as they stand, saying why they are
// not followed further.
func (c *conversation) settle(why string, now time.Time) {
	for _, each := range c.entries {
		if each.working() {
			each.why, each.ended, each.rows = why, now, nil
		}
	}
}

// end shows how a run ended, with what it took.
func (c *conversation) end(status, reason string, now time.Time) {
	c.settle("it never finished", now)
	c.add(&entry{kind: entryEnded, status: status, text: reason, took: c.run, ended: now})
}

// told shows what a session held before what the conversation shows, as if
// its runs had been followed, and counts what they spent.
func (c *conversation) told(events []protocol.Event, now time.Time) {
	earlier := newConversation(c.session, c.label)

	// The context a run starts with is stored before its objective, which a
	// run that is followed shows first.
	events = slices.Clone(events)

	for index := 0; index+1 < len(events); index++ {
		if events[index].Type == protocol.EventContext && events[index+1].Type == protocol.EventObjective {
			events[index], events[index+1] = events[index+1], events[index]
			index++
		}
	}

	for _, event := range events {
		earlier.apply(event, true, now)
	}

	earlier.settle("it never finished", now)
	earlier.close()

	c.entries = append(earlier.entries, c.entries...)
	c.usage = earlier.usage
	c.reply, c.replying = earlier.reply, false
	c.loaded, c.loading = true, false
}

// working reports whether a call shown is still working, which its spinner
// and its clock show.
func (c *conversation) working() bool {
	return slices.ContainsFunc(c.entries, (*entry).working)
}

// rows draws the conversation in rows of width columns at now: each entry,
// apart from the one before by an empty row, unless both are what the run
// did, such as its tool calls, or both are what was said about it.
func (c *conversation) rows(t theme, width int, now time.Time) []string {
	var rows []string

	c.spans = c.spans[:0]

	for index, each := range c.entries {
		if index > 0 && !adjoining(c.entries[index-1].kind, each.kind) {
			rows = append(rows, "")
		}

		from := len(rows)
		rows = append(rows, each.drawn(t, width, now)...)
		c.spans = append(c.spans, span{entry: index, from: from, to: len(rows)})
	}

	return rows
}

// adjoining reports whether entries of two kinds go one under the other
// without an empty row between them.
func adjoining(before, after int) bool {
	work := func(kind int) bool { return kind == entryTool || kind == entryContext }
	said := func(kind int) bool { return kind == entryLog || kind == entryAnswer || kind == entrySaid }

	return work(before) && work(after) || said(before) && said(after) || before == entryTyped && (said(after) || after == entryTyped)
}

// drawn is the entry in rows of width columns, drawn again only when it
// changed, or while it works.
func (e *entry) drawn(t theme, width int, now time.Time) []string {
	if e.working() {
		return []string{cut(e.call.working(t, now), width)}
	}

	if e.rows != nil && e.width == width {
		return e.rows
	}

	e.rows, e.width = e.draw(t, width), width

	return e.rows
}

func (e *entry) draw(t theme, width int) []string {
	switch e.kind {
	case entryTask, entrySteer:
		return bubble(t, e.text, e.images, e.kind == entrySteer, width)

	case entryReply:
		return reply(t, e.text, e.open, width)

	case entryTool:
		switch {
		case !e.finished:
			return e.call.unfinished(e.why, width)
		case e.expanded:
			return e.everything(t, width)
		}

		return e.call.finished(t, e.result, e.failed, e.ended, width)

	case entryContext:
		return []string{cut(styleDim+"context: "+strings.Join(e.sections, ", ")+styleReset, width)}

	case entryEnded:
		return []string{cut(e.took.summary(t, e.status, e.text, e.ended), width)}

	case entryTyped:
		return wrapAll(styleDim+"› "+styleReset+styleBold+e.text+styleReset, width)

	case entryLog:
		return wrapAll(e.label+" "+t.warning+e.text+styleReset, width)

	case entrySaid:
		return wrapAll(e.label+" "+e.text+styleReset, width)
	}

	// An answer: its label on a row of its own, and what it says under it.
	style := styleDim

	if e.failed {
		style = t.failure
	}

	if e.label == "" {
		return wrapAll(style+e.text+styleReset, width)
	}

	return wrapAll(e.label+"\n"+style+e.text+styleReset, width)
}

// wrapAll breaks text, which may hold several lines, into rows of width
// columns, each line keeping its styles across the rows it takes.
func wrapAll(text string, width int) []string {
	var rows []string

	lines := strings.Split(untabbed(text), "\n")
	styles := ""

	for _, line := range lines {
		rows = append(rows, wrap(styles+line, width)...)

		cells, trailing := cellsOf(line)

		for _, each := range cells {
			styles = carried(styles, each.lead)
		}

		styles = carried(styles, trailing)
	}

	return rows
}

// reply draws what the model wrote as the Markdown it is. While it is still
// written, its last line, and a table still coming, show as they stand.
func reply(t theme, text string, open bool, width int) []string {
	shown := markdown{theme: t}
	lines := strings.Split(untabbed(text), "\n")
	partial := lines[len(lines)-1]

	var rows []string

	for _, line := range lines[:len(lines)-1] {
		rows = append(rows, shown.line(line, width)...)
	}

	switch {
	case open && partial != "":
		rows = append(rows, shown.peek(partial, width)...)
	case open:
		rows = append(rows, shown.held()...)
	case partial != "":
		rows = append(rows, shown.line(partial, width)...)
		rows = append(rows, shown.end()...)
	default:
		rows = append(rows, shown.end()...)
	}

	var wrapped []string

	for _, row := range rows {
		wrapped = append(wrapped, wrap(row+styleReset, width)...)
	}

	// The empty lines the model ends with show nothing.
	for len(wrapped) > 0 && visibleWidth(wrapped[len(wrapped)-1]) == 0 {
		wrapped = wrapped[:len(wrapped)-1]
	}

	return wrapped
}

// bubble draws what the person said in a box on the right, as a chat
// shows it, with the images they gave under it, and over what they told a
// run while it worked, a word saying so.
func bubble(t theme, text string, images []protocol.Image, steered bool, width int) []string {
	room := max(min(width*3/4, width-4)-4, 1)

	var inside []string

	for _, line := range strings.Split(untabbed(text), "\n") {
		inside = append(inside, wrap(line, room)...)
	}

	if len(images) > 0 {
		inside = append(inside, wrap(styleDim+imageLabels(images)+styleReset, room)...)
	}

	wide := 0

	for _, row := range inside {
		wide = max(wide, visibleWidth(row))
	}

	indent := strings.Repeat(" ", max(width-wide-4, 0))
	border := styleDim

	var rows []string

	if steered {
		said := "told the run while it worked"
		rows = append(rows, strings.Repeat(" ", max(width-visibleWidth(said), 0))+styleDim+said+styleReset)
	}

	rows = append(rows, indent+border+"╭"+strings.Repeat("─", wide+2)+"╮"+styleReset)

	for _, row := range inside {
		rows = append(rows, indent+border+"│"+styleReset+" "+pad(row, wide)+styleReset+" "+border+"│"+styleReset)
	}

	return append(rows, indent+border+"╰"+strings.Repeat("─", wide+2)+"╯"+styleReset)
}

// untabbed is text with each tab as four spaces: a screen drawn cell by
// cell has no tab stops.
func untabbed(text string) string {
	return strings.ReplaceAll(text, "\t", "    ")
}

// shownLinesAll bounds the lines a call shown whole shows of what it
// returned; a tool's output is bounded anyway, so it is rarely reached.
const shownLinesAll = 1000

// everything draws a finished call with all it was given and all it
// returned, in rows of width columns: its first row as the call shows, the
// arguments, and the whole of an edit's change or of the result.
func (e *entry) everything(t theme, width int) []string {
	rows := e.call.finished(t, e.result, e.failed, e.ended, width)[:1]

	for _, line := range argumentLines(e.call.args) {
		rows = append(rows, cut(styleDim+"    "+untabbed(line)+styleReset, width))
	}

	if e.call.name == "edit" && !e.failed {
		var edit struct {
			Old string `json:"old"`
			New string `json:"new"`
		}

		if json.Unmarshal(e.call.args, &edit) == nil && (edit.Old != "" || edit.New != "") {
			rows = append(rows, styleDim+"    ── what it changed"+styleReset)

			for _, step := range diff(strings.Split(edit.Old, "\n"), strings.Split(edit.New, "\n")) {
				style := styleDim + "  "

				switch step.op {
				case '-':
					style = t.failure + "- "
				case '+':
					style = t.success + "+ "
				}

				rows = append(rows, cut("    "+style+untabbed(step.text)+styleReset, width))
			}
		}
	}

	rows = append(rows, styleDim+"    ── what it returned"+styleReset)
	lines := strings.Split(strings.TrimRight(e.result, "\n"), "\n")
	style := ""

	if e.failed {
		style = t.failure
	}

	for index, line := range lines {
		if index == shownLinesAll {
			rows = append(rows, styleDim+fmt.Sprintf("    … %d more lines", len(lines)-index)+styleReset)
			break
		}

		rows = append(rows, cut("    "+style+untabbed(line)+styleReset, width))
	}

	return rows
}

// argumentLines are a call's arguments for a person to read: each field by
// name, in the order of their names, and a text of several lines under its
// name.
func argumentLines(args json.RawMessage) []string {
	var fields map[string]json.RawMessage

	if json.Unmarshal(args, &fields) != nil {
		if len(args) == 0 {
			return nil
		}

		return strings.Split(string(args), "\n")
	}

	var lines []string

	for _, name := range slices.Sorted(maps.Keys(fields)) {
		value := readable(fields[name])

		if strings.Contains(value, "\n") {
			lines = append(lines, name+":")
			lines = append(lines, strings.Split(value, "\n")...)

			continue
		}

		lines = append(lines, name+": "+value)
	}

	return lines
}

// copied is what copying an entry puts in the clipboard: what the person
// or the model said, or what a call returned, without styles.
func (e *entry) copied() string {
	switch e.kind {
	case entryTool:
		return e.result
	case entryAnswer, entryLog, entrySaid:
		return stripped(e.text)
	}

	return e.text
}
