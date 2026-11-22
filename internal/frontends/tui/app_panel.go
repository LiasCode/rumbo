package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

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
	shown := details{session: a.session, dir: a.info.Dir, model: a.chip(), spent: spentOf(a.shown.usage)}

	for _, listed := range a.sessions.listed {
		if listed.Value == a.session {
			shown.description = listed.Description
		}
	}

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

// conversationKey applies a key while the conversation has it: Up and Down
// choose an entry, Enter acts on it, and Tab or Escape give the keys back
// to the composer, as typing does.
func (a *app) conversationKey(key tea.KeyPressMsg, stroke string) tea.Cmd {
	shown := a.shown

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

	case chosen.kind == entryTask:
		a.palette = a.messageMenu(index)
		return nil
	}

	return a.copy(chosen.copied())
}

// messageMenu offers what can be done with what the person asked.
func (a *app) messageMenu(index int) *palette {
	chosen := a.shown.entries[index]

	var entries []paletteEntry

	if point := a.shown.point(index); point > 0 && a.offers("branch") && a.session != "" {
		entries = append(entries,
			paletteEntry{group: "in a branch that starts before it", name: "Change it", description: "Put it in the composer, to change it and send it", action: actionEdit, point: point, text: chosen.text, images: chosen.images},
			paletteEntry{group: "in a branch that starts before it", name: "Ask it again", description: "Send it as it is", action: actionAskAgain, point: point, text: chosen.text, images: chosen.images},
		)
	}

	entries = append(entries, paletteEntry{group: "this message", name: "Copy it", description: "Put its words in the clipboard", action: actionCopyText, text: chosen.text})

	menu := newPalette(entries)
	menu.title, menu.fixed = "your message", true

	return menu
}

// copy puts text in the clipboard, through the terminal (OSC 52).
func (a *app) copy(text string) tea.Cmd {
	text = strings.TrimRight(text, "\n")

	if strings.TrimSpace(text) == "" {
		a.notice = "there is nothing to copy there"
		return nil
	}

	a.notice = fmt.Sprintf("copied %d line(s), in a terminal that takes OSC 52; in tmux, with set-clipboard on", strings.Count(text, "\n")+1)

	return tea.SetClipboard(text)
}

// branchAt goes on in a branch of the session shown that starts before the
// person's message numbered point, where what it said is put in the
// composer to change, or, again, sent as it is once the branch is shown.
func (a *app) branchAt(point int, text string, images []protocol.Image, again bool) tea.Cmd {
	if a.asking {
		a.notice = "a command is waiting for its answer"
		return nil
	}

	params, _ := json.Marshal(map[string]string{"point": strconv.Itoa(point)})
	line := "/branch " + strconv.Itoa(point)
	a.shown.add(&entry{kind: entryTyped, text: line})
	a.shown.follow = true
	a.asking = true

	then := func() tea.Cmd {
		a.focus = focusComposer

		if !again {
			a.input.set(text)
			a.pasted = images

			return nil
		}

		// The branch shows what it holds before what is asked in it.
		send := func() tea.Cmd { return a.submit(text, images) }

		if a.shown.loaded {
			return send()
		}

		a.afterLoad = send

		return nil
	}

	invoke := a.invoke(fromHost, "branch", params, a.shown, false)

	return func() tea.Msg {
		answered := invoke()

		if msg, ok := answered.(answeredMsg); ok {
			msg.then = then
			return msg
		}

		return answered
	}
}

// sessionEntries are what the palette offers to do with the session shown,
// as rumbo-gui's session menu does, those the host offers; a new session
// has nothing to act on until its first run makes it.
func (a *app) sessionEntries() []paletteEntry {
	var entries []paletteEntry

	add := func(name, description string, action int, needs string) {
		if needs == "" || a.offers(needs) {
			entries = append(entries, paletteEntry{group: "general", name: name, description: description, action: action})
		}
	}

	add("Details", "How the session stands, its context, tokens, and skills (Ctrl-Alt-B)", actionDetails, "")
	add("Changes", "What changed in the project's folder, as Git sees it (Ctrl-G)", actionChanges, "")

	if a.session == "" {
		return entries
	}

	add("Rename this session", "Give it a title of your own", actionRename, "rename")
	add("Branch this session", "Go on in a copy of it; this one stays as it is", actionBranch, "branch")
	add("Compact this session", "Replace its conversation with a summary now", actionCompact, "compact")
	add("Copy the session's id", a.session, actionCopyID, "")
	add("Delete this session", "For good, with its history", actionDelete, "delete")

	return entries
}

// sessionAction does what the palette's entry for the session shown asks.
func (a *app) sessionAction(action int) tea.Cmd {
	function := func(name string) protocol.Function {
		for _, each := range a.info.Functions {
			if each.Name == name {
				return each
			}
		}

		return protocol.Function{Name: name}
	}

	switch action {
	case actionDetails:
		return a.togglePanel(panelDetails)
	case actionChanges:
		return a.togglePanel(panelChanges)
	case actionRename:
		opened := functionForm(function("rename"), fromHost, a.info.Functions, nil)

		if len(opened.fields) > 0 {
			opened.fields[0].value.set(a.sessions.title(a.session))
		}

		return a.openForm(opened)
	case actionBranch:
		return a.run(fromHost, function("branch"), nil, "/branch")
	case actionCompact:
		return a.run(fromHost, function("compact"), nil, "/compact")
	case actionCopyID:
		return a.copy(a.session)
	case actionDelete:
		return a.deleteSession(a.session)
	}

	return nil
}

// deleteSession deletes a session of the project, once the person agrees.
func (a *app) deleteSession(session string) tea.Cmd {
	if session == "" || !a.offers("delete") {
		return nil
	}

	params, _ := json.Marshal(map[string]string{"session": session})

	for _, each := range a.info.Functions {
		if each.Name == "delete" {
			return a.run(fromHost, each, params, "/delete "+session)
		}
	}

	return nil
}
