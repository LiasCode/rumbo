package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/LiasCode/rumbo/internal/frontends/render"
	"github.com/LiasCode/rumbo/protocol"
)

// Beside the conversation, a panel shows the session's details or what
// changed in the project's folder. In the conversation, an entry can be
// chosen to act on it: a call shows all it was given and returned, what
// the person asked can be changed or asked again in a branch that starts
// before it, and anything can be copied.

// The messages of the panels.
type (
	// panelSaidMsg is what the host said of the session for the details,
	// for the reading asked.
	panelSaidMsg struct {
		asked int
		name  string
		text  string
		err   error
	}
	// changesMsg is what Git said changed, for the reading asked.
	changesMsg struct {
		asked   int
		changed changes
		err     error
	}
)

// togglePanel shows a panel, reading what it shows, or puts it away when
// it is the one shown.
func (a *app) togglePanel(kind int) tea.Cmd {
	if a.panels.shown == kind {
		a.panels.shown = panelNone

		if a.focus == focusPanel {
			a.focus = focusComposer
		}

		return nil
	}

	a.panels.shown, a.panels.top = kind, 0

	// Where there is no room for the conversation beside it, the panel
	// takes its place, and the keys.
	if a.layout().covered {
		a.focus = focusPanel
	}

	return a.readPanel()
}

// readPanel reads again what the panel shown shows, since a run or a
// command may have changed it. What was shown stays until it comes.
func (a *app) readPanel() tea.Cmd {
	a.panels.asked++

	asked, conn := a.panels.asked, a.conn

	switch a.panels.shown {
	case panelDetails:
		var cmds []tea.Cmd

		for _, name := range []string{"status", "skills"} {
			slot := &a.panels.status

			if name == "skills" {
				slot = &a.panels.skills
			}

			if !a.offers(name) {
				*slot = reading{}
				continue
			}

			slot.waiting = true

			cmds = append(cmds, func() tea.Msg {
				result, err := conn.InvokeHost(name, nil)

				return panelSaidMsg{asked: asked, name: name, text: strings.TrimSpace(result.Text + "\n" + list(result.Values)), err: err}
			})
		}

		return tea.Batch(cmds...)

	case panelChanges:
		dir := a.info.Dir
		a.panels.changing = true

		return func() tea.Msg {
			changed, err := readChanges(dir)

			return changesMsg{asked, changed, err}
		}
	}

	return nil
}

// panelSaid takes what the host said for the details, unless a later
// reading was asked.
func (a *app) panelSaid(msg panelSaidMsg) {
	if msg.asked != a.panels.asked {
		return
	}

	slot := &a.panels.status

	if msg.name == "skills" {
		slot = &a.panels.skills
	}

	*slot = reading{text: msg.text, err: msg.err}
}

// changed takes what Git said, unless a later reading was asked.
func (a *app) changed(msg changesMsg) {
	if msg.asked != a.panels.asked {
		return
	}

	a.panels.changing = false
	a.panels.changed, a.panels.changedError = &msg.changed, msg.err

	if msg.err != nil {
		a.panels.changed = nil
	}
}

// details are what the app knows of the session shown, for its panel.
func (a *app) details() details {
	shown := details{session: a.session, title: a.called, dir: a.info.Dir, model: a.chip(), spent: spentOf(a.shown.usage)}
	context, window := a.shown.usage.Fill()
	shown.meter = contextMeter(a.theme, context, window)

	return shown
}

// panelKey applies a key while the panel has it: the arrows, PgUp, PgDn,
// Home, and End scroll it, Tab gives the keys back to the composer, and
// Escape puts the panel away.
func (a *app) panelKey(stroke string) tea.Cmd {
	page := max(a.height-bodyTop-4, 1)

	switch stroke {
	case "up":
		a.panels.top = max(a.panels.top-1, 0)
	case "down":
		a.panels.top++
	case "pgup":
		a.panels.top = max(a.panels.top-page, 0)
	case "pgdown":
		a.panels.top += page
	case "home":
		a.panels.top = 0
	case "end":
		a.panels.top = 1 << 30
	case "tab", "shift+tab":
		a.focus = focusComposer
	case "esc":
		a.panels.shown = panelNone
		a.focus = focusComposer
	}

	return nil
}

// conversationKey applies a key while the conversation has it: Up and Down,
// or k and j, choose an entry, K and J the person's messages, gg and G the
// first and the last, Enter acts on the one chosen, c copies it, b branches
// from its message once the person agrees, and Tab or Escape give the keys
// back to the composer, as typing anything else does.
func (a *app) conversationKey(key tea.KeyPressMsg, stroke string) tea.Cmd {
	shown := a.shown

	switch textOf(key) {
	case "k":
		stroke = "up"
	case "j":
		stroke = "down"
	case "K":
		shown.chooseTask(-1)
		a.reveal()

		return nil
	case "J":
		shown.chooseTask(1)
		a.reveal()

		return nil
	case "g":
		if a.pendingG {
			a.pendingG = false
			stroke = "home"
		} else {
			a.pendingG = true

			return nil
		}
	case "G":
		stroke = "end"
	case "c":
		if shown.selected >= 0 && shown.selected < len(shown.entries) {
			return a.copy(shown.entries[shown.selected].copied())
		}

		return nil
	case "b":
		a.confirmBranch(shown.selected)

		return nil
	}

	switch stroke {
	case "up":
		shown.choose(-1)
	case "down":
		shown.choose(1)
	case "home":
		shown.choose(-len(shown.entries))
	case "end":
		shown.choose(len(shown.entries))
	case "enter", "space":
		return a.act(shown.selected)
	case "tab", "shift+tab", "esc":
		a.leaveConversation()

		return nil
	default:
		if textOf(key) != "" {
			a.leaveConversation()

			return a.composerKey(key, stroke)
		}

		return nil
	}

	a.reveal()

	return nil
}

// enterConversation gives the conversation the keys, on its last entry.
func (a *app) enterConversation() {
	a.shown.selected = -1
	a.shown.choose(0)

	if a.shown.selected >= 0 {
		a.focus = focusConversation
		a.reveal()
	}
}

// leaveConversation gives the keys back to the composer.
func (a *app) leaveConversation() {
	a.focus = focusComposer
	a.shown.selected = -1
}

// reveal scrolls the conversation so the entry chosen is in view, its
// first row when it does not fit.
func (a *app) reveal() {
	shown := a.layout()
	top, all := a.conversationTop(shown.column, shown.conversation)
	drawn, ok := a.shown.spanOf(a.shown.selected)

	if !ok {
		return
	}

	last := max(len(all)-shown.conversation, 0)

	switch {
	case drawn.from < top:
		top = drawn.from
	case drawn.to > top+shown.conversation:
		top = min(drawn.to-shown.conversation, drawn.from)
	}

	top = max(min(top, last), 0)
	a.shown.top, a.shown.follow = top, top == last
}

// act acts on an entry of the conversation shown: a call shows all of
// itself or goes back to its excerpt, what the person asked offers to be
// changed or asked again in a branch, and anything else is copied.
func (a *app) act(index int) tea.Cmd {
	if index < 0 || index >= len(a.shown.entries) {
		return nil
	}

	chosen := a.shown.entries[index]

	switch {
	case chosen.kind == entryTool && chosen.finished:
		chosen.expanded, chosen.rows = !chosen.expanded, nil
		a.reveal()

		return nil

	case chosen.kind == entryTask && a.branches() && a.shown.point(index) > 0:
		a.actions = a.messageMenu(index)

		return nil
	}

	return a.copy(chosen.copied())
}

// confirmBranch asks the person to agree to branch the session shown from
// the message of theirs an entry is part of the task of, chosen first so
// it is plain where the branch starts: before that message, which goes in
// the composer there to change or send again.
func (a *app) confirmBranch(index int) {
	if index < 0 || index >= len(a.shown.entries) {
		return
	}

	if !a.branches() {
		a.notice = "this session cannot be branched: it has no run yet, or its host does not branch"
		return
	}

	task := a.shown.taskOf(index)

	if task < 0 {
		a.notice = "there is no message of yours here to branch from"
		return
	}

	a.shown.selected = task
	a.reveal()

	chosen := a.shown.entries[task]
	point := a.shown.point(task)
	quoted := cut(render.FirstLine(strings.TrimSpace(chosen.text)), 60)

	a.confirming = &confirmation{
		function: protocol.Function{
			Name: "branch",
			Confirm: "Branch from your message “" + quoted + "”: a new session holds the conversation as it was before it, and the message goes in the composer there, to change it or send it again. " +
				"This session stays as it is, and so do the files: a branch does not undo what the runs changed on disk.",
		},
		act:  func() tea.Cmd { return a.branchAt(point, chosen.text, chosen.files, false) },
		conv: a.shown,
	}
}

// messageMenu offers what can be done with what the person asked.
func (a *app) messageMenu(index int) *actions {
	chosen, point := a.shown.entries[index], a.shown.point(index)

	return &actions{title: "your message", entries: []action{
		{group: "in a branch that starts before it", name: "Change it", description: "Put it in the composer, to change it and send it", action: actionEdit, point: point, text: chosen.text, files: chosen.files},
		{group: "in a branch that starts before it", name: "Ask it again", description: "Send it as it is", action: actionAskAgain, point: point, text: chosen.text, files: chosen.files},
		{group: "this message", name: "Copy it", description: "Put its words in the clipboard", action: actionCopyText, text: chosen.text},
	}}
}

// copy puts text in the clipboard, and says where once it is there.
func (a *app) copy(text string) tea.Cmd {
	text = strings.TrimRight(text, "\n")

	if strings.TrimSpace(text) == "" {
		a.notice = "there is nothing to copy there"
		return nil
	}

	return a.toClipboard(text, false)
}

// toClipboard sends text to the clipboard, or to the primary selection,
// both through the terminal (OSC 52) and through the desktop.
func (a *app) toClipboard(text string, primary bool) tea.Cmd {
	through, write := tea.SetClipboard(text), a.copier

	if primary {
		through = tea.SetPrimaryClipboard(text)
	}

	lines := strings.Count(text, "\n") + 1

	return tea.Batch(through, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		where, err := write(ctx, text, primary)

		return copiedMsg{lines: lines, where: where, err: err, primary: primary}
	})
}

// copied says where what was copied went: the primary selection goes with
// the clipboard, so only the clipboard says.
func (a *app) copied(msg copiedMsg) {
	switch {
	case msg.primary:
	case errors.Is(msg.err, errNoDesktop):
		a.notice = fmt.Sprintf("sent %d line(s) to the terminal's clipboard (OSC 52); if it does not paste, the terminal does not take it, or tmux needs set-clipboard on", msg.lines)
	case msg.err != nil:
		a.notice = "could not copy: " + msg.err.Error()
	default:
		a.notice = fmt.Sprintf("copied %d line(s) to %s", msg.lines, msg.where)
	}
}

// branchAt goes on in a branch of the session shown that starts before the
// person's message numbered point, or from now without one: its host makes
// the branch, a rumbo is
// started for it, and there what the message said is put in the composer
// to change, or, again, sent as it is once the branch shows what it holds.
func (a *app) branchAt(point int, text string, files []string, again bool) tea.Cmd {
	if a.asking {
		a.notice = "a command is waiting for its answer"
		return nil
	}

	// Without a point, the branch starts from now.
	var params json.RawMessage

	typed := "/branch"

	if point > 0 {
		params, _ = json.Marshal(map[string]string{"point": strconv.Itoa(point)})
		typed += " " + strconv.Itoa(point)
	}

	a.shown.add(&entry{kind: entryTyped, text: typed})
	a.shown.follow = true
	a.asking = true

	then := func() tea.Cmd {
		a.focus = focusComposer

		if !again {
			a.input.set(text)
			a.files = files

			return nil
		}

		// The branch shows what it holds before what is asked in it.
		send := func() tea.Cmd { return a.submit(text, files) }

		if a.shown.loaded {
			return send()
		}

		a.afterLoad = send

		return nil
	}

	invoke := a.invoke(fromHost, "branch", params, a.shown)

	return func() tea.Msg {
		answered := invoke()

		if msg, ok := answered.(answeredMsg); ok {
			msg.then = then
			return msg
		}

		return answered
	}
}
