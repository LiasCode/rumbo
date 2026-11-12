// Package tui is the built-in terminal front end. The conversation scrolls
// in the upper part of the screen and stays in the terminal's own scrollback;
// the last two rows are a status bar and the input line.
package tui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/term"

	"rumbo/internal/frontends/render"
	"rumbo/protocol"
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
	styleGreen   = escape + "[32m"
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
		ui.start("(resuming the unfinished run)", ui.conn.Resume)
	case info.Objective != "":
		ui.submit(info.Objective)
	}

	ui.readKeys(bufio.NewReader(os.Stdin))
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
	input   []rune
	midLine bool
	running bool
	session string
	notice  string
	usage   render.Usage
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

// footer redraws the status bar and the input line, and leaves the cursor at
// the end of the input.
func (u *ui) footer() {
	state := "ready"

	if u.running {
		state = "working (Ctrl-C cancels)"
	}

	if u.notice != "" {
		state = u.notice
	}

	status := fmt.Sprintf(" rumbo | %s/%s | %s", u.info.Provider, u.info.Model, state)

	if u.session != "" {
		status += " | session " + u.session
	}

	status = fit(status+" | "+u.usage.String(), u.width)
	status += strings.Repeat(" ", u.width-len([]rune(status)))

	// The end of a long input stays visible, since that is where typing happens.
	visible := u.input
	room := u.width - len(prompt) - 1

	if len(visible) > room {
		visible = visible[len(visible)-room:]
	}

	fmt.Fprintf(u.out, "%s[%d;1H%s%s%s", escape, u.height-1, styleReverse, status, styleReset)
	fmt.Fprintf(u.out, "%s[%d;1H%s%s%s", escape, u.height, clearLine, prompt, string(visible))
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

// show renders one event from the host.
func (u *ui) show(event protocol.Event) {
	u.mu.Lock()
	defer u.mu.Unlock()

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

	case protocol.EventVerified:
		if event.Passed {
			u.line(styleGreen, "checks passed: "+strings.Join(event.Checks, "; "))
		} else {
			u.line(styleRed, "checks failed:\n"+event.Report)
		}

	case protocol.EventTurn:
		u.usage.Add(event.Usage)
		u.footer()

	case protocol.EventLog:
		u.line(styleYellow, event.Text)
	}
}

// submit starts a run on objective, unless one is already in progress.
func (u *ui) submit(objective string) {
	u.start(objective, func(onEvent func(protocol.Event)) (protocol.RunResult, error) {
		return u.conn.Run(objective, onEvent)
	})
}

// start follows one run, shown under label, unless one is in progress.
func (u *ui) start(label string, run func(onEvent func(protocol.Event)) (protocol.RunResult, error)) {
	u.mu.Lock()

	if u.running {
		u.notice = "still working; Ctrl-C cancels"
		u.footer()
		u.mu.Unlock()

		return
	}

	u.running = true
	u.notice = ""
	u.input = nil
	u.line(styleBold, "\n"+prompt+label)
	u.mu.Unlock()

	u.wait.Add(1)

	go func() {
		defer u.wait.Done()

		result, err := run(u.show)

		u.mu.Lock()
		defer u.mu.Unlock()

		u.running = false
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
	}()
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

// key applies one key press and reports whether to keep running.
func (u *ui) key(key rune) bool {
	u.mu.Lock()

	switch key {
	case '\r', '\n':
		objective := strings.TrimSpace(string(u.input))
		u.mu.Unlock()

		if objective != "" {
			u.submit(objective)
		}

		return true

	case 0x03: // Ctrl-C: cancel the run, else clear the input, else quit.
		if u.running {
			u.conn.Cancel()
		} else if len(u.input) > 0 {
			u.input = nil
		} else {
			u.mu.Unlock()
			return false
		}

	case 0x04: // Ctrl-D: quit.
		if len(u.input) == 0 {
			u.mu.Unlock()
			u.conn.Cancel()

			return false
		}

	case 0x7f, 0x08:
		if len(u.input) > 0 {
			u.input = u.input[:len(u.input)-1]
		}

	case 0x15: // Ctrl-U: clear the line.
		u.input = nil

	case 0x17: // Ctrl-W: delete the last word.
		text := strings.TrimRight(string(u.input), " ")
		u.input = []rune(text[:strings.LastIndex(text, " ")+1])

	case 0x0c: // Ctrl-L: redraw.
		u.measure()
		u.layout(u.height - 2)
		u.midLine = false

	default:
		if key >= 0x20 {
			u.input = append(u.input, key)
		}
	}

	u.notice = ""
	u.footer()
	u.mu.Unlock()

	return true
}

// readSequence consumes the rest of an escape sequence, so arrow keys and
// the like do not end up in the input as text.
func readSequence(keys *bufio.Reader) (string, error) {
	first, _, err := keys.ReadRune()

	if err != nil {
		return "", err
	}

	if first != '[' && first != 'O' {
		return "", keys.UnreadRune()
	}

	sequence := string(first)

	for {
		next, _, err := keys.ReadRune()

		if err != nil {
			return "", err
		}

		sequence += string(next)

		if next >= 0x40 && next <= 0x7e {
			return sequence, nil
		}

		if len(sequence) > 32 {
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
