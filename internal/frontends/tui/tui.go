// Package tui is the built-in terminal front end. The conversation scrolls
// in the upper part of the screen and stays in the terminal's own scrollback;
// the last two rows are a status bar and the input line.
package tui

import (
	"bufio"
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

	"golang.org/x/term"

	"github.com/LiasCode/rumbo/internal/frontends/render"
	"github.com/LiasCode/rumbo/protocol"
)

// Terminal control sequences.
const (
	escape       = "\x1b"
	saveCursor   = escape + "7"
	loadCursor   = escape + "8"
	clearLine    = escape + "[2K"
	resetRegion  = escape + "[r"
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

	ui := &ui{conn: conn, info: info, out: os.Stdout, session: info.Session}

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
	out  io.Writer
	wait sync.WaitGroup

	mu      sync.Mutex
	width   int
	height  int
	input   inputLine
	midLine bool
	// history is what was entered here, oldest first, for Up and Down to
	// bring back. recalled is the line of it in the input, len(history)
	// when none is, and draft is what was being typed before going back.
	history  []string
	recalled int
	draft    string
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
	// model and options are what the user chose here for the runs to come;
	// empty means the stored values.
	model   string
	options map[string]json.RawMessage
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

// open pushes what is on the screen into the scrollback and draws the
// layout on a clean screen.
func (u *ui) open() {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.measure()

	fmt.Fprint(u.out, strings.Repeat("\r\n", u.height), pasteOn)
	u.layout(1)
}

// close gives the terminal back as an ordinary one, below the conversation.
func (u *ui) close() {
	u.mu.Lock()
	defer u.mu.Unlock()

	fmt.Fprintf(u.out, "%s%s%s[%d;1H%s%s[%d;1H%s", pasteOff, resetRegion, escape, u.height-1, clearLine, escape, u.height, clearLine)
	fmt.Fprintf(u.out, "%s[%d;1H", escape, u.height-1)
}

func (u *ui) resize() {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.measure()
	u.layout(u.height - 2)
	u.midLine = false
}

func (u *ui) measure() {
	width, height, err := term.GetSize(int(os.Stdout.Fd()))

	if err != nil || width < 20 || height < 5 {
		width, height = 80, 24
	}

	u.width, u.height = width, height
}

// layout confines scrolling to the rows above the footer and puts the output
// cursor on row. The terminal keeps what scrolls off the top.
func (u *ui) layout(row int) {
	fmt.Fprintf(u.out, "%s[1;%dr%s[%d;1H%s", escape, u.height-2, escape, row, saveCursor)
	u.footer()
}

// footer redraws the status bar and the input line, and leaves the cursor
// where it is in the input.
func (u *ui) footer() {
	state := "ready"

	switch {
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

	// While a command is being typed, the bar shows what it could become.
	if typed := u.input.String(); strings.HasPrefix(typed, "/") && !strings.Contains(typed, " ") {
		if matches := narrow(u.commandNames(), typed); len(matches) > 0 {
			state = "Tab: " + suggest(matches)
		}
	}

	if u.notice != "" {
		state = u.notice
	}

	model := u.info.Model

	if u.model != "" {
		model = u.model
	}

	status := fmt.Sprintf(" rumbo | provider %s, model %s | %s", u.info.Provider, model, state)

	if len(u.away) > 0 {
		status += fmt.Sprintf(" | %d working in the background", len(u.away))
	}

	if u.session != "" {
		status += " | session " + u.session
	}

	if context := u.usage.Context(); context != "" {
		status += " | " + context
	}

	status = fit(status+" | "+u.usage.String(), u.width)
	status += strings.Repeat(" ", u.width-len([]rune(status)))

	// A line wider than the screen shows the part around the cursor.
	room := u.width - len([]rune(prompt))
	visible, column := u.input.view(room, u.question != nil && u.question.secret)

	fmt.Fprintf(u.out, "%s[%d;1H%s%s%s", escape, u.height-1, styleReverse, status, styleReset)
	fmt.Fprintf(u.out, "%s[%d;1H%s%s%s", escape, u.height, clearLine, prompt, visible)
	fmt.Fprintf(u.out, "%s[%d;%dH", escape, u.height, len([]rune(prompt))+column+1)
}

// write adds text to the conversation at the output cursor.
func (u *ui) write(text string) {
	if text == "" {
		return
	}

	fmt.Fprint(u.out, loadCursor, strings.ReplaceAll(text, "\n", "\r\n"), saveCursor)
	u.midLine = !strings.HasSuffix(text, "\n")
	u.footer()
}

// line adds a styled line of its own to the conversation.
func (u *ui) line(style, text string) {
	lead := ""

	if u.midLine {
		lead = "\n"
	}

	u.write(lead + style + text + styleReset + "\n")
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
	u.line(styleBold, "\n"+prompt+label)
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

		result, err := u.conn.Launch(u.choices(params), named, shown)

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
			u.line(styleBold, "\n"+prompt+text)
		case u.input.empty():
			u.line(styleRed, err.Error())
			u.input.set(text)
		default:
			u.line(styleRed, err.Error()+": "+text)
		}

		u.footer()
	})
}

// readKeys edits the input line until the user quits or stdin ends.
func (u *ui) readKeys(keys *bufio.Reader) {
	pasting := false

	for {
		key, _, err := keys.ReadRune()

		if err != nil {
			u.conn.Cancel()
			return
		}

		if key == 0x1b {
			sequence, err := readSequence(keys)

			if err != nil {
				return
			}

			switch sequence {
			case "[200~":
				pasting = true
			case "[201~":
				pasting = false

			// Up and Down move through what was entered, as Ctrl-P and
			// Ctrl-N do.
			case "[A", "OA":
				u.key(0x10)
			case "[B", "OB":
				u.key(0x0e)

			// Left, Right, Home, and End move in the line, as Ctrl-B,
			// Ctrl-F, Ctrl-A, and Ctrl-E do.
			case "[D", "OD":
				u.key(0x02)
			case "[C", "OC":
				u.key(0x06)
			case "[H", "OH", "[1~", "[7~":
				u.key(0x01)
			case "[F", "OF", "[4~", "[8~":
				u.key(0x05)

			case "[3~": // Delete.
				u.edit((*inputLine).remove)

			// With Ctrl or Alt, Left and Right move by words, as Alt-B
			// and Alt-F do.
			case "[1;5D", "[1;3D", "b":
				u.edit((*inputLine).wordLeft)
			case "[1;5C", "[1;3C", "f":
				u.edit((*inputLine).wordRight)
			}

			continue
		}

		// A pasted line break is part of the text, not a submit.
		if pasting && (key == '\r' || key == '\n') {
			key = ' '
		}

		if !u.key(key) {
			return
		}
	}
}

// edit changes the input line, for keys that do nothing else.
func (u *ui) edit(change func(*inputLine)) {
	u.mu.Lock()
	defer u.mu.Unlock()

	change(&u.input)
	u.notice = ""
	u.footer()
}

// key applies one key press and reports whether to keep running.
func (u *ui) key(key rune) bool {
	u.mu.Lock()

	switch key {
	case '\r', '\n':
		// The line answers what a function asked, when it asked something.
		if asked := u.question; asked != nil {
			u.answer(asked)
			u.mu.Unlock()

			return true
		}

		line, running := strings.TrimSpace(u.input.String()), u.here != nil

		// The answer to a confirmation is not worth bringing back.
		if line != "" && u.pending == nil {
			u.remember(line)
		}

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

	case '\t': // Tab: complete a command or its argument.
		line := u.input.String()
		answering := u.question != nil
		u.mu.Unlock()

		if !strings.HasPrefix(line, "/") || answering {
			return true
		}

		// Completing may ask the host for values, so it runs unlocked.
		completed, hint := u.complete(line)

		u.mu.Lock()
		u.input.set(completed)
		u.notice = hint
		u.footer()
		u.mu.Unlock()

		return true

	case 0x03: // Ctrl-C: cancel the function or the run here, else clear the input, else quit.
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

	case 0x04: // Ctrl-D: quit on an empty line, else delete under the cursor.
		if u.input.empty() {
			u.mu.Unlock()
			u.conn.Cancel()

			return false
		}

		u.input.remove()

	case 0x7f, 0x08: // Backspace.
		u.input.backspace()

	case 0x01: // Ctrl-A, or Home: to the start of the line.
		u.input.home()

	case 0x05: // Ctrl-E, or End: to the end of the line.
		u.input.end()

	case 0x02: // Ctrl-B, or Left.
		u.input.left()

	case 0x06: // Ctrl-F, or Right.
		u.input.right()

	case 0x15: // Ctrl-U: delete to the start of the line.
		u.input.killStart()

	case 0x0b: // Ctrl-K: delete to the end of the line.
		u.input.killEnd()

	case 0x17: // Ctrl-W: delete the word before the cursor.
		u.input.deleteWord()

	case 0x0c: // Ctrl-L: redraw.
		u.measure()
		u.layout(u.height - 2)
		u.midLine = false

	case 0x10: // Ctrl-P, or Up: the line entered before.
		u.recall(-1)

	case 0x0e: // Ctrl-N, or Down: the line entered after, or the one being typed.
		u.recall(1)

	default:
		if key >= 0x20 {
			u.input.insert(key)
		}
	}

	u.notice = ""
	u.footer()
	u.mu.Unlock()

	return true
}

// remember adds a line that was entered to the history, unless it repeats
// the one before, and leaves the history at its end. The caller holds the
// lock.
func (u *ui) remember(line string) {
	if last := len(u.history) - 1; last < 0 || u.history[last] != line {
		u.history = append(u.history, line)
	}

	u.recalled = len(u.history)
	u.draft = ""
}

// recall puts another line of the history in the input: an older one for a
// step back, a newer one for a step forward, and past the newest what was
// being typed. The caller holds the lock.
func (u *ui) recall(step int) {
	next := u.recalled + step

	if next < 0 || next > len(u.history) {
		return
	}

	if u.recalled == len(u.history) {
		u.draft = u.input.String()
	}

	u.recalled = next

	if next == len(u.history) {
		u.input.set(u.draft)
	} else {
		u.input.set(u.history[next])
	}
}

// readSequence consumes the rest of an escape sequence, so arrow keys and
// the like do not end up in the input as text. Alt-B and Alt-F come as the
// letter alone.
func readSequence(keys *bufio.Reader) (string, error) {
	first, _, err := keys.ReadRune()

	if err != nil {
		return "", err
	}

	if first == 'b' || first == 'f' {
		return string(first), nil
	}

	if first != '[' && first != 'O' {
		return "", keys.UnreadRune()
	}

	var sequence strings.Builder
	sequence.WriteString(string(first))

	for {
		next, _, err := keys.ReadRune()

		if err != nil {
			return "", err
		}

		sequence.WriteString(string(next))

		if next >= 0x40 && next <= 0x7e {
			return sequence.String(), nil
		}

		if len(sequence.String()) > 32 {
			return "", errors.New("escape sequence too long")
		}
	}
}

// fit shortens text to width columns.
func fit(text string, width int) string {
	runes := []rune(text)

	if len(runes) <= width {
		return text
	}

	return string(runes[:width-1]) + "…"
}
