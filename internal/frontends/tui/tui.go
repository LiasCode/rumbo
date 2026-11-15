// Package tui is the built-in terminal front end. It draws inline: the
// conversation goes into the terminal's own scrollback like the output of
// any command, and a live area under it shows what is still changing: the
// text the model is writing, the menu of what a command can become, the
// input, and the status bar.
package tui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/term"

	"github.com/LiasCode/rumbo/internal/frontends/render"
	"github.com/LiasCode/rumbo/protocol"
)

// Terminal control sequences.
const (
	escape       = "\x1b"
	pasteOn      = escape + "[?2004h"
	pasteOff     = escape + "[?2004l"
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

	conn, info, err := protocol.DialFrontend(r, w, "tui")

	if err != nil {
		fmt.Fprintln(os.Stderr, "rumbo:", err)
		return 1
	}

	saved, err := term.MakeRaw(input)

	if err != nil {
		fmt.Fprintln(os.Stderr, "rumbo:", err)
		return 1
	}

	ui := &ui{
		conn:    conn,
		info:    info,
		screen:  screen{out: os.Stdout},
		session: info.Session,
		history: &history{},
	}

	// What was asked before comes back from the host, which keeps it with
	// the sessions. A host that keeps nothing to recall leaves it empty.
	if recalled, err := conn.Recall(); err == nil {
		ui.history.earlier(recalled.Asked)
	}

	defer func() {
		ui.close()
		_ = term.Restore(input, saved)
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

	switch {
	case info.Resume:
		ui.start("(resuming the unfinished run)", protocol.RunParams{Resume: true})
	case info.Objective != "":
		ui.submit(info.Objective)
	}

	// The front end ends when the user quits, or when the host goes away,
	// which is also how a quit asked of the host arrives.
	quit := make(chan struct{})

	go func() {
		ui.readKeys(bufio.NewReader(os.Stdin))
		close(quit)
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
	// partial is what the model wrote of a line it has not ended yet, which
	// the live area shows until the line is done.
	partial string
	history *history
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

// job is a run this front end started, and the session it works in. A run
// in a new session learns its session from its first event.
type job struct {
	// run is the id of the run request, which names the run to the host.
	run     int64
	session string
}

// open draws the live area where the cursor is, below what the terminal
// showed before.
func (u *ui) open() {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.measure()
	fmt.Fprint(u.screen.out, pasteOn)
	u.footer()
}

// close gives the terminal back as an ordinary one, below the conversation.
func (u *ui) close() {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.flush()
	u.screen.clear()
	fmt.Fprint(u.screen.out, pasteOff)
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
	width := u.screen.width
	var rows []string

	// A long line still being written shows its end.
	if u.partial != "" {
		written := wrap(u.partial, width-1)
		rows = append(rows, written[max(len(written)-u.maxInputRows(), 0):]...)
	}

	if u.menu != nil {
		rows = append(rows, u.menu.rows(width)...)
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

	rows = append(rows, u.status(width))

	u.screen.paint(done, rows, top+row, len([]rune(prompt))+column)
}

// status is the status bar.
func (u *ui) status(width int) string {
	state := "ready"

	switch {
	case u.searching:
		state = fmt.Sprintf("search: %s  (Ctrl-R older, Enter takes it, Escape leaves)", u.query)

		if u.found < 0 && u.query != "" {
			state = fmt.Sprintf("search: %s  (nothing holds it)", u.query)
		}
	case u.question != nil && u.question.secret:
		state = "type the answer, which is not shown; Enter sends it, Ctrl-C gives up"
	case u.question != nil:
		state = "type the answer; Enter sends it, Ctrl-C gives up"
	case u.here != nil && u.telling > 0:
		state = fmt.Sprintf("working; %d message(s) wait for its next turn (Ctrl-C cancels)", u.telling)
	case u.here != nil:
		state = "working (Ctrl-C cancels)"
	case u.asking:
		state = "waiting for the answer"
	}

	if u.notice != "" {
		state = u.notice
	}

	status := fmt.Sprintf(" rumbo | provider %s, model %s | %s", u.info.Provider, u.info.Model, state)

	if len(u.away) > 0 {
		status += fmt.Sprintf(" | %d working in the background", len(u.away))
	}

	if u.session != "" {
		status += " | session " + u.session
	}

	if context := u.usage.Context(); context != "" {
		status += " | " + context
	}

	return styleReverse + pad(cut(status+" | "+u.usage.String(), width), width) + styleReset
}

// write adds text the model wrote to the conversation. Whole lines go into
// the scrollback; the rest waits in the live area for the line to end.
func (u *ui) write(text string) {
	u.partial += text
	done := ""

	if end := strings.LastIndexByte(u.partial, '\n'); end >= 0 {
		done, u.partial = u.partial[:end+1], u.partial[end+1:]
	}

	u.paint(done)
}

// line adds a styled line of its own to the conversation, after what the
// model was writing.
func (u *ui) line(style, text string) {
	done := ""

	if u.partial != "" {
		done, u.partial = u.partial+"\n", ""
	}

	u.paint(done + style + text + styleReset + "\n")
}

// flush ends the line the model was writing, if there is one.
func (u *ui) flush() {
	if u.partial != "" {
		done := u.partial + "\n"
		u.partial = ""
		u.paint(done)
	}
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
		return
	}

	switch event.Type {
	case protocol.EventText:
		u.write(event.Text)

	case protocol.EventContext:
		u.line(styleDim, "context: "+strings.Join(event.Sections, ", "))

	case protocol.EventToolStarted:
		u.line(styleDim, "  "+render.Tool(event.Name, event.Args))

	case protocol.EventToolFinished:
		if event.Failed {
			u.line(styleRed, "  failed: "+render.FirstLine(event.Result))
		}

	case protocol.EventTurn:
		u.usage.Turn(event)
		u.footer()

	case protocol.EventLog:
		// A log line is the provider's own output or something the host
		// did; which one is said, since they call for different fixes.
		source := fromHost

		if event.Source == protocol.TargetProvider {
			source = fromProvider
		}

		u.line("", u.label(source)+" "+styleYellow+event.Text)
	}
}

// submit starts a run on objective, unless one is already in progress.
func (u *ui) submit(objective string) {
	u.start(objective, protocol.RunParams{Objective: objective})
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

	started := &job{session: u.session}
	u.here = started
	u.notice = ""
	u.input.clear()
	u.line(styleBold, "\n"+echoed(label))
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

	if done != u.here {
		u.away = slices.DeleteFunc(u.away, func(other *job) bool { return other == done })

		outcome := result.Status

		switch {
		case err != nil:
			outcome = err.Error()
		case result.Status == protocol.RunFailed:
			outcome += ": " + result.Error
		}

		u.line("", u.label(fromHost)+" "+styleDim+"session "+done.session+" finished: "+outcome)
		u.footer()

		return
	}

	u.here = nil
	u.notice = ""
	u.flush()

	switch {
	case err != nil:
		u.line(styleRed, err.Error())
	case result.Status == protocol.RunFailed:
		u.line(styleRed, result.Error)
	case result.Status == protocol.RunCancelled:
		u.line(styleYellow, "cancelled")
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

// steer tells the run in progress text. It is shown once the model has it,
// which is where the model reads it. If the run ends first, the text goes
// back to the input line, to be sent as a task.
func (u *ui) steer(text string) {
	u.mu.Lock()

	// The run may have ended since Enter was pressed.
	if u.here == nil {
		u.input.set(text)
		u.notice = "the run ended before this reached it; Enter sends it as a new task"
		u.footer()
		u.mu.Unlock()

		return
	}

	run := u.here.run
	u.input.clear()
	u.notice = ""
	u.telling++
	u.footer()
	u.mu.Unlock()

	u.wait.Go(func() {

		err := u.conn.SteerRun(run, text)

		u.mu.Lock()
		defer u.mu.Unlock()

		u.telling--

		switch {
		case err == nil:
			u.line(styleBold, "\n"+echoed(text))
		case u.input.empty():
			u.line(styleRed, err.Error())
			u.input.set(text)
		default:
			u.line(styleRed, err.Error()+": "+text)
		}

		u.footer()
	})
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
		case !u.input.empty():
			u.input.clear()
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

	case pressed.r == 0x7f, pressed.r == 0x08: // Backspace.
		u.input.backspace()

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

			if u.needsMore(u.input.String()) {
				u.refreshMenu()
				u.footer()
				u.mu.Unlock()

				return true
			}
		}
	}

	line, running := strings.TrimSpace(u.input.String()), u.here != nil

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
	case line != "" && running:
		u.steer(line)
	case line != "":
		u.submit(line)
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
