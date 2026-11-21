// Package tui is the built-in terminal front end. It draws inline: the
// conversation goes into the terminal's own scrollback like the output of
// any command, and a live area under it shows what is still changing: the
// text the model is writing, the menu of what a command can become, the
// input, and the status bar.
package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/LiasCode/rumbo/internal/frontends/render"
	"github.com/LiasCode/rumbo/protocol"
)

// Terminal control sequences.
const (
	escape       = "\x1b"
	pasteOn      = escape + "[?2004h"
	pasteOff     = escape + "[?2004l"
	focusOn      = escape + "[?1004h"
	focusOff     = escape + "[?1004l"
	styleReset   = escape + "[0m"
	styleBold    = escape + "[1m"
	styleDim     = escape + "[2m"
	styleRed     = escape + "[31m"
	styleCyan    = escape + "[36m"
	styleMagenta = escape + "[35m"
	styleYellow  = escape + "[33m"
	styleReverse = escape + "[7m"
)

const prompt = "> "

// echoed is what the person typed as the conversation shows it: after the
// prompt, with the lines after the first under the first.
func echoed(text string) string {
	return prompt + strings.ReplaceAll(text, "\n", "\n"+strings.Repeat(" ", len([]rune(prompt))))
}

// Run attaches to the host on r and w, takes over the terminal, and returns
// the process exit code.
func Run(r io.Reader, w io.Writer) int {
	input := int(os.Stdin.Fd())

	if !term.IsTerminal(input) || !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(os.Stderr, "rumbo: the tui front end needs a terminal; use -f plain")
		return 1
	}

	// The configuration in force is told to the host whether it is right or
	// not, so it can be seen and changed where it is registered.
	var config Config
	var wrong error

	conn, info, err := protocol.DialFrontendConfigured(r, w, "tui", func(given json.RawMessage) json.RawMessage {
		config, wrong = configured(given)
		inForce, _ := json.Marshal(config)

		return inForce
	})

	if err != nil {
		fmt.Fprintln(os.Stderr, "rumbo:", err)
		return 1
	}

	colors, err := themeOf(config, os.Getenv("NO_COLOR"))

	if err = errors.Join(wrong, err); err != nil {
		fmt.Fprintf(os.Stderr, "rumbo: the terminal UI's configuration: %v\nrumbo frontend set tui <field>=<value> changes it\n", err)
		return 1
	}

	saved, err := term.MakeRaw(input)

	if err != nil {
		fmt.Fprintln(os.Stderr, "rumbo:", err)
		return 1
	}

	ui := &ui{
		conn:     conn,
		info:     info,
		screen:   screen{out: os.Stdout},
		session:  info.Session,
		history:  &history{},
		theme:    colors,
		markdown: markdown{theme: colors},
	}

	// What was asked before comes back from the host, which keeps it with
	// the sessions. A host that keeps nothing to recall leaves it empty.
	if recalled, err := conn.Recall(); err == nil {
		ui.history.earlier(recalled.Asked)
	}

	defer func() {
		ui.close()
		_ = term.Restore(input, saved)

		if leaving := ui.leaving(); leaving != "" {
			fmt.Fprintln(os.Stdout, leaving)
		}
	}()

	ui.open()

	resized := make(chan os.Signal, 1)
	signal.Notify(resized, syscall.SIGWINCH)

	go func() {
		for range resized {
			ui.resize()
		}
	}()

	// The provider's own commands are known once it says what it offers,
	// which is asked for at once, so the menu has them from the start.
	ui.wait.Go(func() { _, _ = ui.describe() })

	// A session continued or chosen at the start shows what it holds, which
	// the host tells before a run it is asked for next starts.
	if info.Session != "" {
		if told, err := conn.History(info.Session); err == nil {
			ui.mu.Lock()
			ui.retell(told)
			ui.mu.Unlock()
		}
	}

	switch {
	case info.Resume:
		ui.start("(resuming the unfinished run)", protocol.RunParams{Resume: true})
	case info.Objective != "":
		ui.submit(info.Objective, nil)
	}

	// The front end ends when the user quits, or when the host goes away,
	// which is also how a quit asked of the host arrives.
	quit := make(chan struct{})

	go func() {
		ui.readKeys(bufio.NewReader(os.Stdin))
		close(quit)
	}()

	go func() {
		ticks := time.NewTicker(120 * time.Millisecond)
		defer ticks.Stop()

		for {
			select {
			case <-quit:
				return
			case <-ticks.C:
				ui.tick()
			}
		}
	}()

	select {
	case <-quit:
	case <-conn.Done():
	}

	ui.wait.Wait()

	return 0
}

// ui is the screen state. Every method that writes takes the lock, because
// events arrive on another goroutine than keys.
type ui struct {
	conn *protocol.FrontendConn
	info protocol.FrontendInitializeParams
	wait sync.WaitGroup

	mu     sync.Mutex
	screen screen
	input  inputLine
	// pasted are the images Ctrl-V put in the input, which go with the next
	// task or steer; clipboard reads the image to paste, when a test does
	// not want the desktop's.
	pasted    []protocol.Image
	clipboard func(context.Context) ([]byte, error)
	// partial is what the model wrote of a line it has not ended yet, which
	// the live area shows until the line is done, and markdown shows what
	// the model writes as the Markdown it is.
	partial  string
	markdown markdown
	// tool is the call in progress, which the live area shows until it is
	// done, and theme the colors everything is shown in.
	tool  *running
	theme theme
	// reply is the model's last answer, what it wrote since the last thing
	// that was not text, which /copy takes; replying is true while it goes
	// on.
	reply    string
	replying bool
	// last is the kind of what went into the scrollback last, and blank is
	// true when that ended with an empty line.
	last  int
	blank bool
	// closed is true once the terminal was given back, and nothing is drawn
	// any more, and unfocused while the terminal says its window is not in
	// front.
	closed    bool
	unfocused bool
	history   *history
	// searching is true while Ctrl-R looks back through the history for
	// query; found is the entry shown, and before what the input held when
	// the search began.
	searching bool
	query     string
	found     int
	before    string
	// menu offers what the command being typed can become; dismissed says
	// Escape closed it, until the input changes.
	menu      *menu
	dismissed bool
	// here is the run in the current session, which the screen follows, and
	// away are the runs left working in other sessions.
	here *job
	away []*job
	// asking is true while a command waits for its answer. Commands can be
	// asked during a run; the host says which ones wait for it.
	asking bool
	// telling counts what was typed to the run and has not reached the
	// model yet.
	telling int
	session string
	// described is what the provider said it offers, once it was asked.
	described *protocol.DescribeResult
	// pending is a function that asked to be confirmed and waits for a yes.
	pending func() (answer, error)
	// invoking is the id of the provider's function in progress, which
	// Ctrl-C cancels, and question is what that function asked, waiting for
	// the next line typed.
	invoking int64
	question *question
	notice   string
	usage    render.Usage
}

// question is what a function asked the person, and where the answer goes.
// A secret one is not shown while it is typed, nor kept.
type question struct {
	secret bool
	answer chan string
}

// The kinds of what goes into the scrollback. Where text and what the run
// did meet, and before how a run ended, a blank line sets them apart.
const (
	blockOther = iota
	blockText
	blockWork
	blockEnd
)

// job is a run this front end started, and the session it works in. A run
// in a new session learns its session from its first event.
type job struct {
	// run is the id of the run request, which names the run to the host.
	run     int64
	session string
	// label is what the run was asked, as the conversation shows it.
	label string
	// tally is what the run took so far. A run told by a session's history,
	// rather than followed, has no time.
	tally tally
	told  bool
}

// open draws the live area where the cursor is, below what the terminal
// showed before.
func (u *ui) open() {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.measure()
	fmt.Fprint(u.screen.out, pasteOn+focusOn)
	u.footer()
}

// close gives the terminal back as an ordinary one, below the conversation.
func (u *ui) close() {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.flush()
	u.screen.clear()
	fmt.Fprint(u.screen.out, pasteOff+focusOff)
	u.closed = true
}

// leaving is what stays below the conversation once the terminal UI has
// quit: the command that continues its session, or nothing when it has none.
func (u *ui) leaving() string {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.session == "" {
		return ""
	}

	return styleDim + "rumbo -s " + u.session + " continues this session" + styleReset
}

// notify tells the person that something they may be waiting for happened
// while the terminal was not in front of them: with the bell, which
// terminals and tmux show on their window, and as a desktop notification in
// the terminals that take OSC 9. It does nothing while the terminal is in
// front, or when it never said it left. The caller holds the lock.
func (u *ui) notify(text string) {
	if !u.unfocused || u.closed {
		return
	}

	plain := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}

		return r
	}, text)

	fmt.Fprint(u.screen.out, "\a"+escape+"]9;"+plain+"\a")
}

// tick draws again what is working, for its spinner and its clock.
func (u *ui) tick() {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.here != nil || u.tool != nil {
		u.footer()
	}
}

func (u *ui) resize() {
	u.mu.Lock()
	defer u.mu.Unlock()

	width, height := size()
	u.screen.resized(width, height)
	u.footer()
}

func (u *ui) measure() {
	u.screen.width, u.screen.height = size()
}

func size() (int, int) {
	width, height, err := term.GetSize(int(os.Stdout.Fd()))

	if err != nil || width < 20 || height < 5 {
		return 80, 24
	}

	return width, height
}

// maxInputRows is how many rows the input may take before it scrolls.
func (u *ui) maxInputRows() int {
	return max(min(10, u.screen.height/3), 1)
}

// footer draws the live area again.
func (u *ui) footer() {
	u.paint("")
}

// paint writes done, which goes into the scrollback, and draws the live
// area under it: what the model is writing, the menu, the input, and the
// status bar.
func (u *ui) paint(done string) {
	if u.closed {
		return
	}

	if done != "" {
		u.blank = endsBlank(done)
	}

	width, now := u.screen.width, time.Now()
	var rows []string

	// A long line still being written shows its end, and a table still
	// coming shows as it is so far.
	writing := u.markdown.held()

	if u.partial != "" {
		writing = u.markdown.peek(u.partial, width)
	}

	var written []string

	for _, row := range writing {
		written = append(written, wrap(row, width-1)...)
	}

	rows = append(rows, written[max(len(written)-u.maxInputRows(), 0):]...)

	if u.tool != nil {
		rows = append(rows, cut(u.tool.working(u.theme, now), width-1))
	}

	if u.menu != nil {
		rows = append(rows, u.menu.rows(width)...)
	}

	if len(u.pasted) > 0 {
		rows = append(rows, cut(styleDim+imageLabels(u.pasted)+"  Backspace on an empty line takes the last away"+styleWeightOff, width-1))
	}

	room := width - len([]rune(prompt))
	masked := u.question != nil && u.question.secret
	typed, row, column := u.input.view(room, u.maxInputRows(), masked)
	top := len(rows)

	for index, text := range typed {
		lead := strings.Repeat(" ", len([]rune(prompt)))

		if index == 0 && u.input.offset == 0 {
			lead = styleBold + prompt + styleReset
		}

		rows = append(rows, lead+text)
	}

	rows = append(rows, u.status(width, now))

	u.screen.paint(done, rows, top+row, len([]rune(prompt))+column)
}

// status is the status bar: what is happening, the model, the session, and
// the runs in the background on the left, and how full the context is and
// what the session spent on the right. What does not fit goes, the least
// needed first.
func (u *ui) status(width int, now time.Time) string {
	state := u.state(now)
	model := u.info.Provider + "/" + u.info.Model
	session, away := "", ""

	// A session can start before its model is chosen, which /use does.
	if u.info.Model == "" {
		model = u.info.Provider + ", " + u.theme.warning + "no model" + styleColorOff
	}

	if u.session != "" {
		session = "session " + u.session
	}

	if len(u.away) > 0 {
		away = u.theme.warning + fmt.Sprintf("%d in the background", len(u.away)) + styleColorOff
	}

	context, window := u.usage.Fill()
	meter := contextMeter(u.theme, context, window)
	total := u.usage.Total()
	spent := ""

	if render.Input(total)+total.Output > 0 {
		spent = fmt.Sprintf("%s in, %s cached, %s out", tokens(render.Input(total)), tokens(total.CacheRead), tokens(total.Output))
	}

	for _, parts := range [][2][]string{
		{{state, model, session, away}, {meter, spent}},
		{{state, model, session, away}, {meter}},
		{{state, model, away}, {meter}},
		{{state, model, away}, nil},
		{{state}, nil},
	} {
		left, right := " "+together(parts[0]), together(parts[1])

		if right != "" {
			right += " "
		}

		if gap := width - visibleWidth(left) - visibleWidth(right); gap >= 2 || parts[1] == nil && gap >= 0 {
			return left + strings.Repeat(" ", max(gap, 0)) + right
		}
	}

	return cut(" "+state, width)
}

// state says what is happening, and what keys do about it.
func (u *ui) state(now time.Time) string {
	switch {
	case u.notice != "":
		return u.theme.warning + u.notice + styleColorOff
	case u.searching && u.found < 0 && u.query != "":
		return fmt.Sprintf("search: %s  %s(nothing holds it)%s", u.query, styleDim, styleWeightOff)
	case u.searching:
		return fmt.Sprintf("search: %s  %s(Ctrl-R older, Enter takes it, Escape leaves)%s", u.query, styleDim, styleWeightOff)
	case u.question != nil && u.question.secret:
		return u.theme.warning + "type the answer, which is not shown; Enter sends it, Ctrl-C gives up" + styleColorOff
	case u.question != nil:
		return u.theme.warning + "type the answer; Enter sends it, Ctrl-C gives up" + styleColorOff
	case u.here != nil:
		working := u.theme.progress + spin(now) + styleColorOff + " working " + elapsed(now.Sub(u.here.tally.started))

		if u.telling > 0 {
			working += fmt.Sprintf("; %d message(s) wait for its next turn", u.telling)
		}

		return working + styleDim + "  Ctrl-C cancels" + styleWeightOff
	case u.asking:
		return "waiting for the answer"
	}

	return styleDim + "ready" + styleWeightOff
}

// together joins the parts there are, apart.
func together(parts []string) string {
	var there []string

	for _, part := range parts {
		if part != "" {
			there = append(there, part)
		}
	}

	return strings.Join(there, styleDim+" · "+styleWeightOff)
}

// contextMeter shows how full the model's context is: a bar and a share
// when its size is known, colored as it fills, and a count of tokens when
// it is not.
func contextMeter(t theme, context, window int64) string {
	switch {
	case context == 0:
		return ""
	case window <= 0:
		return "context " + tokens(context)
	}

	share := min(context*100/window, 100)
	// A full bar is a full context.
	filled := int(share / 10)
	color := ""

	switch {
	case share >= 80:
		color = t.failure
	case share >= 50:
		color = t.warning
	}

	bar := color + strings.Repeat("▰", filled) + styleColorOff + styleDim + strings.Repeat("▱", 10-filled) + styleWeightOff

	return fmt.Sprintf("%s %s%d%%%s of %s", bar, color, share, styleColorOff, tokens(window))
}

// write adds text the model wrote to the conversation. Whole lines go into
// the scrollback as the Markdown they are; the rest waits in the live area
// for the line to end.
func (u *ui) write(text string) {
	u.partial += text

	var rows []string

	for {
		end := strings.IndexByte(u.partial, '\n')

		if end < 0 {
			break
		}

		rows = append(rows, u.markdown.line(u.partial[:end], u.screen.width)...)
		u.partial = u.partial[end+1:]
	}

	if len(rows) > 0 {
		rows = u.apart(blockText, rows)
	}

	u.paint(joined(rows))
}

// line adds a styled line of its own to the conversation, after what the
// model was writing.
func (u *ui) line(style, text string) {
	u.rows(blockOther, []string{style + text})
}

// rows adds rows of their own, of a kind, to the conversation, after what
// the model was writing.
func (u *ui) rows(kind int, rows []string) {
	done := u.ended()

	if done != "" {
		u.blank = endsBlank(done)
	}

	u.paint(done + joined(u.apart(kind, rows)))
}

// endsBlank reports whether whole lines end with an empty one.
func endsBlank(done string) bool {
	lines := strings.Split(strings.TrimSuffix(done, "\n"), "\n")

	return visibleWidth(lines[len(lines)-1]) == 0
}

// apart puts a blank line before rows of a kind that follow another, and
// keeps the kind as the last one. The caller holds the lock.
func (u *ui) apart(kind int, rows []string) []string {
	if kind != blockOther && u.last != blockOther && u.last != kind && !u.blank {
		rows = append([]string{""}, rows...)
	}

	u.last = kind

	return rows
}

// flush ends what the model was writing, if it was.
func (u *ui) flush() {
	if done := u.ended(); done != "" {
		u.paint(done)
	}
}

// ended takes what the model wrote that is not shown yet, as whole lines:
// the line it had not ended, and a table still coming.
func (u *ui) ended() string {
	var rows []string

	if u.partial != "" {
		rows = u.markdown.line(u.partial, u.screen.width)
		u.partial = ""
	}

	rows = append(rows, u.markdown.end()...)

	if len(rows) == 0 {
		return ""
	}

	return joined(u.apart(blockText, rows))
}

// joined is rows as the scrollback takes them, each ended and its styles
// with it.
func joined(rows []string) string {
	var out strings.Builder

	for _, row := range rows {
		out.WriteString(row + styleReset + "\n")
	}

	return out.String()
}

// show renders one event of a run.
func (u *ui) show(from *job, event protocol.Event) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if from.session == "" && event.Session != "" {
		from.session = event.Session

		if from == u.here {
			u.session = event.Session
		}
	}

	// A run in the background shows nothing until it ends. What belongs to
	// no run, such as a provider's diagnostics, is shown whatever run it
	// came with.
	if from != u.here && event.Run != 0 {
		if event.Type == protocol.EventTurn {
			from.tally.add(event.Usage)
		}

		return
	}

	u.tell(from, event)
}

// tell shows one event of a run, followed as it works or told by a
// session's history, which also tells what each objective was and how
// each run ended. The caller holds the lock.
func (u *ui) tell(from *job, event protocol.Event) {
	if event.Type != protocol.EventText && event.Type != protocol.EventTurn {
		u.replying = false
	}

	switch event.Type {
	case protocol.EventText:
		if !u.replying {
			u.reply, u.replying = "", true
		}

		u.reply += event.Text
		u.write(event.Text)

	case protocol.EventContext:
		u.rows(blockWork, []string{styleDim + "context: " + strings.Join(event.Sections, ", ")})

	case protocol.EventToolStarted:
		u.settle()
		u.flush()
		u.tool = &running{name: event.Name, args: event.Args}

		if !from.told {
			u.tool.started = time.Now()
		}

		u.footer()

	case protocol.EventToolFinished:
		// A call of a run that was in the background when it started is
		// known only by its name.
		if u.tool == nil || u.tool.name != event.Name {
			u.tool = &running{name: event.Name}
		}

		done := u.tool.finished(u.theme, event.Result, event.Failed, time.Now(), u.screen.width)
		u.tool = nil
		u.rows(blockWork, done)

	case protocol.EventTurn:
		from.tally.add(event.Usage)
		u.usage.Turn(event)
		u.footer()

	case protocol.EventObjective, protocol.EventSteer:
		u.settle()

		if event.Type == protocol.EventObjective {
			from.tally = tally{}
		}

		u.said(event.Text, event.Images)

	case protocol.EventEnded:
		u.settle()
		u.rows(blockEnd, []string{from.tally.summary(u.theme, event.Status, event.Text, time.Now())})

	case protocol.EventLog:
		// A log line is the provider's own output or something the host
		// did; which one is said, since they call for different fixes.
		source := fromHost

		if event.Source == protocol.TargetProvider {
			source = fromProvider
		}

		u.line("", u.label(source)+" "+u.theme.warning+event.Text)
	}
}

// settle shows a call told as started that will not finish. The caller
// holds the lock.
func (u *ui) settle() {
	u.leave("it never finished")
}

// leave puts the call in progress in the scrollback as it stands, saying
// why it is not followed further. The caller holds the lock.
func (u *ui) leave(why string) {
	if u.tool != nil {
		done := u.tool.unfinished(why, u.screen.width)
		u.tool = nil
		u.rows(blockWork, done)
	}
}

// retell shows what a session held, as if its runs had been followed, and
// counts what they spent. The caller holds the lock.
func (u *ui) retell(told protocol.HistoryResult) {
	if told.Session == "" || len(told.Events) == 0 {
		return
	}

	heading := "── session " + told.Session + " "
	u.line(styleDim, "\n"+heading+strings.Repeat("─", max(min(u.screen.width-1, 60)-visibleWidth(heading), 3)))

	from := &job{session: told.Session, told: true}

	// The context a run starts with is stored before its objective, which a
	// run that is followed shows first.
	events := slices.Clone(told.Events)

	for index := 0; index+1 < len(events); index++ {
		if events[index].Type == protocol.EventContext && events[index+1].Type == protocol.EventObjective {
			events[index], events[index+1] = events[index+1], events[index]
			index++
		}
	}

	for _, event := range events {
		u.tell(from, event)
	}

	u.settle()
	u.flush()
}

// submit starts a run on objective and the images given with it, unless
// one is already in progress.
func (u *ui) submit(objective string, images []protocol.Image) {
	u.start(objective, protocol.RunParams{Objective: objective, Images: images})
}

// start follows one run in the current session, shown under label, unless
// one is in progress there.
func (u *ui) start(label string, params protocol.RunParams) {
	u.mu.Lock()

	refused := ""

	switch {
	case u.here != nil:
		refused = "still working; Ctrl-C cancels"
	case u.asking:
		refused = "a command is waiting for its answer"
	}

	if refused != "" {
		u.notice = refused
		u.footer()
		u.mu.Unlock()

		return
	}

	started := &job{session: u.session, label: label, tally: tally{started: time.Now()}}
	u.here = started
	u.notice = ""
	u.input.clear()
	u.pasted = nil
	u.said(label, params.Images)
	u.mu.Unlock()

	u.wait.Go(func() {

		named := func(run int64) {
			u.mu.Lock()
			defer u.mu.Unlock()

			started.run = run
		}

		shown := func(event protocol.Event) {
			u.show(started, event)
		}

		result, err := u.conn.Launch(params, named, shown)

		u.mu.Lock()
		defer u.mu.Unlock()

		u.end(started, result, err)
	})
}

// end shows how a run ended. One that worked in the background says so in
// a line of its own, naming its session. The caller holds the lock.
func (u *ui) end(done *job, result protocol.RunResult, err error) {
	if result.Session != "" {
		done.session = result.Session
	}

	outcome := result.Status

	switch {
	case err != nil:
		outcome = err.Error()
	case result.Status == protocol.RunFailed:
		outcome += ": " + result.Error
	}

	u.notify("rumbo: " + outcome + " · " + render.FirstLine(done.label))

	if done != u.here {
		u.away = slices.DeleteFunc(u.away, func(other *job) bool { return other == done })

		ended := done.tally.summary(u.theme, result.Status, result.Error, time.Now())

		if err != nil {
			ended = u.theme.failure + err.Error() + styleColorOff
		}

		u.rows(blockOther, []string{u.theme.warning + "◆" + styleColorOff + " session " + done.session + ", in the background: " + ended + styleDim + "  /open " + done.session + " shows it"})
		u.footer()

		return
	}

	u.here = nil
	u.notice = ""
	u.settle()
	u.flush()

	if err != nil {
		u.line(u.theme.failure, err.Error())
	} else {
		u.rows(blockEnd, []string{done.tally.summary(u.theme, result.Status, result.Error, time.Now())})
	}

	if result.Session != "" {
		u.session = result.Session
	}

	u.footer()
}

// follow sorts the runs after the current session changed: the one working
// in the current session is shown, and the others go on in the background.
// A run whose new session is not made yet stays where it was. The caller
// holds the lock.
func (u *ui) follow() {
	here, runs := u.here, u.away

	// What the run left behind was showing goes into the scrollback as it
	// stands, and the rest of it is not shown.
	defer func() {
		if u.here != here {
			u.flush()
			u.leave("it goes on in the background")
		}
	}()

	if here != nil {
		runs = append(runs, here)
	}

	u.here, u.away = nil, nil

	for _, run := range runs {
		if run.session == u.session && (run.session != "" || run == here) {
			u.here = run
		} else {
			u.away = append(u.away, run)
		}
	}
}

// steer tells the run in progress text, and images. It is shown once the
// model has it, which is where the model reads it. If the run ends first,
// what was said goes back to the input, to be sent as a task.
func (u *ui) steer(text string, images []protocol.Image) {
	u.mu.Lock()

	// The run may have ended since Enter was pressed.
	if u.here == nil {
		u.input.set(text)
		u.pasted = images
		u.notice = "the run ended before this reached it; Enter sends it as a new task"
		u.footer()
		u.mu.Unlock()

		return
	}

	run := u.here.run
	u.input.clear()
	u.pasted = nil
	u.notice = ""
	u.telling++
	u.footer()
	u.mu.Unlock()

	u.wait.Go(func() {

		err := u.conn.SteerRun(run, text, images...)

		u.mu.Lock()
		defer u.mu.Unlock()

		u.telling--

		switch {
		case err == nil:
			u.said(text, images)
		case u.input.empty() && len(u.pasted) == 0:
			u.line(u.theme.failure, err.Error())
			u.input.set(text)
			u.pasted = images
		default:
			u.line(u.theme.failure, err.Error()+": "+text)
		}

		u.footer()
	})
}

// said shows what the person said, and the images they gave with it, in
// the conversation. The caller holds the lock.
func (u *ui) said(text string, images []protocol.Image) {
	u.line(styleBold, "\n"+echoed(text))

	if len(images) > 0 {
		u.line(styleDim, strings.Repeat(" ", len([]rune(prompt)))+imageLabels(images))
	}
}

// readKeys edits the input until the user quits or stdin ends.
func (u *ui) readKeys(keys *bufio.Reader) {
	pasting := false

	for {
		pressed, err := readKey(keys)

		if err != nil {
			u.conn.Cancel()
			return
		}

		switch {
		case pressed.name == keyPasteStart:
			pasting = true
			continue
		case pressed.name == keyPasteEnd:
			pasting = false
			continue

		// What is pasted is text, its line breaks and tabs included: none
		// of it sends or completes anything.
		case pasting && (pressed.r == '\r' || pressed.r == '\n'):
			pressed = key{name: keyNewline}
		case pasting && pressed.r == '\t':
			pressed = key{r: ' '}
		}

		if !u.press(pressed) {
			return
		}
	}
}

// press applies one key and reports whether to keep running.
func (u *ui) press(pressed key) bool {
	u.mu.Lock()

	if pressed.name == keyFocusIn || pressed.name == keyFocusOut {
		u.unfocused = pressed.name == keyFocusOut
		u.mu.Unlock()

		return true
	}

	if u.searching {
		u.searchKey(pressed)
		u.mu.Unlock()

		return true
	}

	open := u.menu != nil && len(u.menu.shown) > 0
	before := u.input.String()
	browsed := false

	switch {
	case pressed.r == '\r':
		return u.enter(open)

	case pressed.name == keyNewline, pressed.r == '\n': // Alt-Enter, Shift-Enter, or Ctrl-J.
		u.input.insert('\n')

	case pressed.r == '\t':
		if open {
			u.take()
		} else {
			// Tab opens the menu Escape closed.
			u.dismissed = false
		}

	case pressed.name == keyBackTab && open:
		u.menu.move(-1)

	case pressed.name == keyUp && open:
		u.menu.move(-1)

	case pressed.name == keyDown && open:
		u.menu.move(1)

	case pressed.name == keyPageUp && open:
		u.menu.move(-maxChoices)

	case pressed.name == keyPageDown && open:
		u.menu.move(maxChoices)

	// Up and Down go through the rows of what is typed, and past its first
	// or last row through what was entered before.
	case pressed.name == keyUp:
		if !u.input.up() {
			browsed = u.recall(-1)
		}

	case pressed.name == keyDown:
		if !u.input.down() {
			browsed = u.recall(1)
		}

	case pressed.name == keyEscape:
		u.menu, u.dismissed = nil, true

	case pressed.name == keyLeft, pressed.r == 0x02: // Left, or Ctrl-B.
		u.input.left()

	case pressed.name == keyRight, pressed.r == 0x06: // Right, or Ctrl-F.
		u.input.right()

	case pressed.name == keyHome, pressed.r == 0x01: // Home, or Ctrl-A: to the start of the line.
		u.input.home()

	case pressed.name == keyEnd, pressed.r == 0x05: // End, or Ctrl-E: to the end of the line.
		u.input.end()

	case pressed.name == keyWordLeft:
		u.input.wordLeft()

	case pressed.name == keyWordRight:
		u.input.wordRight()

	case pressed.name == keyDelete:
		u.input.remove()

	case pressed.r == 0x03: // Ctrl-C: give up the function or cancel the run here, else clear the input, else quit.
		switch {
		case u.invoking != 0:
			u.conn.CancelInvoke(u.invoking)
		case u.here != nil:
			// The id is known as soon as the run is sent; until then, a
			// second press cancels it.
			if u.here.run != 0 {
				u.conn.CancelRun(u.here.run)
			}
		case !u.input.empty() || len(u.pasted) > 0:
			u.input.clear()
			u.pasted = nil
		default:
			u.mu.Unlock()
			return false
		}

	case pressed.r == 0x04: // Ctrl-D: quit on an empty line, else delete under the cursor.
		if u.input.empty() {
			u.mu.Unlock()
			u.conn.Cancel()

			return false
		}

		u.input.remove()

	case pressed.r == 0x7f, pressed.r == 0x08: // Backspace, which takes the last image away on an empty line.
		if u.input.empty() && len(u.pasted) > 0 {
			u.pasted = u.pasted[:len(u.pasted)-1]
		} else {
			u.input.backspace()
		}

	case pressed.r == 0x16: // Ctrl-V: an image from the clipboard, which a terminal does not paste.
		u.pasteImage()

	case pressed.r == 0x15: // Ctrl-U: delete to the start of the line.
		u.input.killStart()

	case pressed.r == 0x0b: // Ctrl-K: delete to the end of the line.
		u.input.killEnd()

	case pressed.r == 0x17: // Ctrl-W: delete the word before the cursor.
		u.input.deleteWord()

	case pressed.r == 0x12: // Ctrl-R: look back through what was entered.
		u.searching, u.query, u.found, u.before = true, "", len(u.history.entries), u.input.String()
		u.menu = nil

	case pressed.r == 0x0c: // Ctrl-L: start the screen again, the conversation kept in the scrollback.
		u.measure()
		u.screen.fresh()

	case pressed.r == 0x10: // Ctrl-P: the line entered before.
		browsed = u.recall(-1)

	case pressed.r == 0x0e: // Ctrl-N: the line entered after, or the one being typed.
		browsed = u.recall(1)

	case pressed.name == "" && pressed.r >= 0x20:
		u.input.insert(pressed.r)
	}

	// The menu follows what is typed, and comes back once it changes. A
	// command brought back from the history opens none, so Up and Down go
	// on through the history.
	switch {
	case browsed:
		u.dismissed = true
	case u.input.String() != before:
		u.dismissed = false
	}

	u.notice = ""
	u.refreshMenu()
	u.footer()
	u.mu.Unlock()

	return true
}

// enter sends what is typed: the answer to what a function asked, a
// command, or a task, which goes to the run in progress when there is one.
// With the menu open on a choice that is not typed yet, it takes the choice
// first, and sends the line only when the command needs nothing more. The
// caller holds the lock, which enter lets go of.
func (u *ui) enter(open bool) bool {
	// The line answers what a function asked, when it asked something.
	if asked := u.question; asked != nil {
		u.answer(asked)
		u.mu.Unlock()

		return true
	}

	if open {
		_, token := splitLast(u.input.String())

		if chosen, ok := u.menu.current(); ok && chosen.value != token {
			u.take()

			// A name= waits for its value, or for Enter again to send it
			// empty.
			if u.needsMore(u.input.String()) || strings.HasSuffix(chosen.value, "=") {
				u.refreshMenu()
				u.footer()
				u.mu.Unlock()

				return true
			}
		}
	}

	// A command that still needs something opens the menu of what it takes
	// next, rather than failing for the want of it.
	if typed := u.input.String(); u.pending == nil && isCommand(strings.TrimSpace(typed)) && u.needsMore(typed) {
		if !strings.HasSuffix(typed, " ") {
			u.input.set(typed + " ")
		}

		u.dismissed = false
		u.refreshMenu()
		u.footer()
		u.mu.Unlock()

		return true
	}

	line, running := strings.TrimSpace(u.input.String()), u.here != nil
	images := u.pasted
	said := line != "" || len(images) > 0

	// The answer to a confirmation is not worth bringing back.
	if line != "" && u.pending == nil {
		u.history.add(line)
	}

	u.menu = nil
	u.mu.Unlock()

	switch {
	case u.confirm(line):
	case isCommand(line):
		u.command(line)
	case said && running:
		u.steer(line, images)
	case said:
		u.submit(line, images)
	}

	return true
}

// take puts the choice selected in the menu in place of the word being
// typed. The caller holds the lock.
func (u *ui) take() {
	chosen, ok := u.menu.current()

	if !ok {
		return
	}

	head, _ := splitLast(u.input.String())

	// A name= is taken to go on with its value.
	if strings.HasSuffix(chosen.value, "=") {
		u.input.set(head + chosen.value)
		return
	}

	u.input.set(head + chosen.value + " ")
}

// searchKey applies a key while Ctrl-R looks back through the history: what
// is typed narrows it, Ctrl-R goes further back, Enter takes the entry
// found into the input, and Escape leaves it as it was. Any other key takes
// the entry too. The caller holds the lock.
func (u *ui) searchKey(pressed key) {
	look := func(from int) {
		if entry, index := u.history.search(u.query, from); index >= 0 {
			u.found = index
			u.input.set(entry)
		} else {
			u.found = -1
		}
	}

	switch {
	case pressed.name == "" && pressed.r >= 0x20:
		u.query += string(pressed.r)
		look(len(u.history.entries))

	case pressed.r == 0x7f || pressed.r == 0x08:
		if runes := []rune(u.query); len(runes) > 0 {
			u.query = string(runes[:len(runes)-1])
		}

		look(len(u.history.entries))

	case pressed.r == 0x12:
		if u.found > 0 {
			look(u.found)
		}

	case pressed.name == keyEscape, pressed.r == 0x07, pressed.r == 0x03:
		u.input.set(u.before)
		u.searching = false

	// What is taken from the history opens no menu until it is edited.
	default:
		u.searching, u.dismissed = false, true
	}

	u.footer()
}

// recall puts another line of the history in the input, and reports
// whether there was one. The caller holds the lock.
func (u *ui) recall(step int) bool {
	line, ok := u.history.recall(step, u.input.String())

	if ok {
		u.input.set(line)
	}

	return ok
}

// edit changes the input, for keys that do nothing else.
func (u *ui) edit(change func(*inputLine)) {
	u.mu.Lock()
	defer u.mu.Unlock()

	change(&u.input)
	u.notice = ""
	u.footer()
}
