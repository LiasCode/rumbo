package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// The keys are readline's where they edit what is typed, and rumbo-gui's
// where they act on the window: Ctrl-B shows or puts away the sidebar,
// Ctrl-N starts a new session, Ctrl-G and Ctrl-Alt-B show the changes and
// the details, and Escape cancels. The pointer chooses, scrolls, and
// selects text, which is copied as its button comes up.

// press applies one key.
func (a *app) press(key tea.KeyPressMsg) tea.Cmd {
	stroke := key.Keystroke()
	a.notice = ""
	a.unselect()

	// gg is two keys in a row; anything else between them leaves one g.
	if textOf(key) != "g" {
		a.pendingG = false
	}

	// What floats takes every key while it is open.
	if cmd, taken := a.floatingKey(key, stroke); taken {
		return cmd
	}

	switch stroke {
	// Ctrl-P starts a command, as a slash on an empty line does.
	case "ctrl+p":
		if !a.input.empty() {
			a.notice = "the commands start with a slash on an empty line"
			return nil
		}

		a.focus = focusComposer
		a.input.set("/")
		a.dismissed = false

		return a.refreshMenu()

	case "ctrl+b":
		a.sidebar = !a.sidebar

		if !a.sidebar && a.focus == focusSidebar {
			a.focus = focusComposer
		}

		return nil

	case "ctrl+n":
		return a.reach("", nil)

	case "ctrl+g":
		return a.togglePanel(panelChanges)

	case "ctrl+alt+b":
		return a.togglePanel(panelDetails)

	case "pgup", "pgdown":
		if a.focus != focusPanel {
			step := a.page()

			if stroke == "pgup" {
				step = -step
			}

			a.scroll(step)

			return nil
		}
	}

	switch {
	case a.focus == focusSidebar:
		return a.sidebarKey(key, stroke)
	case a.focus == focusPanel:
		return a.panelKey(stroke)
	case a.focus == focusConversation:
		return a.conversationKey(key, stroke)
	case a.searching:
		a.searchKey(key, stroke)

		return nil
	}

	return a.composerKey(key, stroke)
}

// textOf is the text a key types, if it types any.
func textOf(key tea.KeyPressMsg) string {
	if key.Mod&(tea.ModCtrl|tea.ModAlt) != 0 {
		return ""
	}

	return key.Text
}

// edit applies a key that moves in a line of text or edits it, with
// readline's keys, and reports whether it was one. The composer and every
// line of what floats take them.
func edit(line *inputLine, key tea.KeyPressMsg, stroke string) bool {
	switch stroke {
	case "left":
		line.left()
	case "right", "ctrl+f":
		line.right()
	case "home", "ctrl+a":
		line.home()
	case "end", "ctrl+e":
		line.end()
	case "ctrl+left", "alt+left", "alt+b":
		line.wordLeft()
	case "ctrl+right", "alt+right", "alt+f":
		line.wordRight()
	case "backspace", "ctrl+h":
		line.backspace()
	case "delete":
		line.remove()
	case "ctrl+u":
		line.killStart()
	case "ctrl+k":
		line.killEnd()
	case "ctrl+w", "alt+backspace":
		line.deleteWord()
	default:
		text := textOf(key)

		if text == "" {
			return false
		}

		for _, r := range text {
			line.insert(r)
		}
	}

	return true
}

// composerKey applies a key to the composer.
func (a *app) composerKey(key tea.KeyPressMsg, stroke string) tea.Cmd {
	open := a.menu != nil && len(a.menu.shown) > 0
	before := a.input.String()
	browsed := false

	var cmd tea.Cmd

	switch stroke {
	case "enter":
		return a.enter()

	case "shift+enter", "alt+enter", "ctrl+j":
		a.input.insert('\n')

	case "tab":
		switch {
		case open:
			a.take()

		// Tab on an empty composer goes to the sessions, or to the panel
		// without them.
		case a.input.empty() && len(a.files) == 0 && a.layout().side > 0:
			a.focus = focusSidebar
			a.sessions.choose(a.session)
			a.sessions.reveal(a.height - bodyTop)

			return nil

		case a.input.empty() && len(a.files) == 0 && a.panels.shown != panelNone:
			a.focus = focusPanel

			return nil

		// Tab opens the menu Escape closed.
		default:
			a.dismissed = false
		}

	// Shift-Tab goes up into the conversation, to act on what it holds.
	case "shift+tab":
		if open {
			a.menu.move(-1)
		} else {
			a.enterConversation()
		}

	// Up and Down choose in the menu, or go through the rows of what is
	// typed, and past its first or last row through what was entered
	// before.
	case "up":
		switch {
		case open:
			a.menu.move(-1)
		case !a.input.up():
			browsed = a.recall(-1)
		}

	case "down":
		switch {
		case open:
			a.menu.move(1)
		case !a.input.down():
			browsed = a.recall(1)
		}

	// Escape closes the menu, else gives up the provider's function in
	// progress, else cancels the run in the session shown.
	case "esc":
		switch {
		case a.menu != nil:
			a.menu, a.dismissed = nil, true
		default:
			a.cancel()
		}

	// Ctrl-C gives up the function or cancels the run here, else clears
	// the composer, else quits.
	case "ctrl+c":
		switch {
		case a.invoking != 0 || a.here() != nil:
			a.cancel()
		case !a.input.empty() || len(a.files) > 0:
			a.input.clear()
			a.files = nil
		default:
			return tea.Quit
		}

	// Ctrl-D quits on an empty composer, cancelling what works in every
	// session, else deletes under the cursor.
	case "ctrl+d":
		if a.input.empty() {
			for _, held := range a.hosts {
				held.conn.Cancel()
			}

			return tea.Quit
		}

		a.input.remove()

	// Backspace on an empty composer takes the last file away.
	case "backspace", "ctrl+h":
		if a.input.empty() && len(a.files) > 0 {
			a.files = a.files[:len(a.files)-1]
		} else {
			a.input.backspace()
		}

	case "ctrl+v":
		cmd = a.pasteImage()

	case "ctrl+r":
		a.searching, a.query, a.found, a.before = true, "", len(a.history.entries), a.input.String()
		a.menu = nil

	// The rest moves in what is typed, or edits it, as in any line of text.
	default:
		edit(&a.input, key, stroke)
	}

	// The menu follows what is typed, and comes back once it changes. A
	// command brought back from the history opens none, so Up and Down go
	// on through the history.
	switch {
	case browsed:
		a.dismissed = true
	case a.input.String() != before:
		a.dismissed = false
	}

	return tea.Batch(cmd, a.refreshMenu())
}

// cancel gives up the provider's function in progress, else cancels the run
// in the session shown. The run's id is known as soon as it is sent; until
// then, a second press cancels it.
func (a *app) cancel() {
	switch run := a.here(); {
	case a.invoking != 0:
		a.conn.CancelInvoke(a.invoking)
	case run != nil && run.id != 0:
		a.conn.CancelRun(run.id)
	}
}

// sidebarKey applies a key while the sidebar has the keyboard: Up and Down
// choose a session, Enter shows it, and Tab or Escape go back to the
// composer, as typing does.
func (a *app) sidebarKey(key tea.KeyPressMsg, stroke string) tea.Cmd {
	body := a.height - bodyTop

	// The keys that choose in the conversation choose here too, and d
	// deletes the session chosen, once the person agrees.
	switch textOf(key) {
	case "k":
		stroke = "up"
	case "j":
		stroke = "down"
	case "g":
		if !a.pendingG {
			a.pendingG = true

			return nil
		}

		a.pendingG = false
		stroke = "home"
	case "G":
		stroke = "end"
	case "d":
		if session := a.sessions.chosen(); session != "" {
			return a.deleteSession(session)
		}

		return nil
	}

	switch stroke {
	case "up":
		a.sessions.move(-1, body)

	case "down":
		a.sessions.move(1, body)

	case "home":
		a.sessions.move(-len(a.sessions.listed), body)

	case "end":
		a.sessions.move(len(a.sessions.listed), body)

	case "enter":
		return a.open()

	// Tab goes on to the panel, when one is shown, and back to the
	// composer.
	case "tab":
		a.focus = focusComposer

		if a.panels.shown != panelNone {
			a.focus = focusPanel
		}

	case "shift+tab", "esc", "ctrl+c":
		a.focus = focusComposer

	default:
		if textOf(key) != "" {
			a.focus = focusComposer

			return a.composerKey(key, stroke)
		}
	}

	return nil
}

// searchKey applies a key while Ctrl-R looks back through the history: what
// is typed narrows it, Ctrl-R goes further back, and Escape leaves the
// composer as it was. Any other key takes the entry found.
func (a *app) searchKey(key tea.KeyPressMsg, stroke string) {
	look := func(from int) {
		if entry, index := a.history.search(a.query, from); index >= 0 {
			a.found = index
			a.input.set(entry)
		} else {
			a.found = -1
		}
	}

	switch {
	case textOf(key) != "":
		a.query += textOf(key)
		look(len(a.history.entries))

	case stroke == "backspace":
		if runes := []rune(a.query); len(runes) > 0 {
			a.query = string(runes[:len(runes)-1])
		}

		look(len(a.history.entries))

	case stroke == "ctrl+r":
		if a.found > 0 {
			look(a.found)
		}

	case stroke == "esc", stroke == "ctrl+g", stroke == "ctrl+c":
		a.input.set(a.before)
		a.searching = false

	// What is taken from the history opens no menu until it is edited.
	default:
		a.searching, a.dismissed = false, true
	}
}

// recall puts another line of the history in the composer, and reports
// whether there was one.
func (a *app) recall(step int) bool {
	line, ok := a.history.recall(step, a.input.String())

	if ok {
		a.input.set(line)
	}

	return ok
}

// wheel scrolls what is under the pointer.
func (a *app) wheel(mouse tea.Mouse) {
	step := 3

	if mouse.Button == tea.MouseWheelUp {
		step = -3
	} else if mouse.Button != tea.MouseWheelDown {
		return
	}

	switch shown := a.layout(); {
	// Over anything that floats, the wheel does nothing.
	case a.floating():
	case mouse.X < shown.side:
		a.sessions.scroll(step/3, a.height-bodyTop)
	case shown.panel > 0 && mouse.X >= shown.panelX:
		a.panels.top = max(a.panels.top+step, 0)
	default:
		a.scroll(step)
	}
}

// click shows the session clicked in the sidebar, gives the panel the keys
// when it is clicked, acts on an entry of the conversation as Enter does on
// a call or on what the person asked, and chooses any other, opens the
// picker on the model under the composer, and gives the composer the
// keyboard when it is clicked. A click goes to nothing while something
// floats.
func (a *app) click(mouse tea.Mouse) tea.Cmd {
	if mouse.Button != tea.MouseLeft || a.floating() {
		return nil
	}

	shown := a.layout()

	if mouse.X < shown.side && mouse.Y >= bodyTop {
		if index, ok := a.sessions.at(mouse.Y - bodyTop); ok {
			a.sessions.selected = index

			return a.open()
		}

		return nil
	}

	if shown.panel > 0 && mouse.X >= shown.panelX {
		a.focus = focusPanel
		return nil
	}

	inColumn := mouse.X >= shown.columnX && mouse.X < shown.columnX+shown.column

	if inColumn && mouse.Y >= bodyTop && mouse.Y < bodyTop+shown.conversation {
		top, _ := a.conversationTop(shown.column, shown.conversation)

		if index, ok := a.shown.at(top + mouse.Y - bodyTop); ok && selectable(a.shown.entries[index].kind) {
			a.shown.selected, a.focus = index, focusConversation

			if kind := a.shown.entries[index].kind; kind == entryTool || kind == entryTask {
				return a.act(index)
			}
		}

		return nil
	}

	// The model is at the start of the composer's last row, in its border.
	footer := shown.composerTop + len(shown.composer) - 2
	from := shown.columnX + 2

	if mouse.Y == footer && mouse.X >= from && mouse.X < from+visibleWidth(a.chip()) {
		return a.openPicker()
	}

	if mouse.Y >= shown.composerTop && !shown.covered {
		a.leaveConversation()
	}

	return nil
}

// scroll moves the conversation shown by rows, up for fewer than none; at
// its end, it follows what comes.
func (a *app) scroll(rows int) {
	shown := a.layout()
	top, all := a.conversationTop(shown.column, shown.conversation)
	last := max(len(all)-shown.conversation, 0)

	top = max(min(top+rows, last), 0)
	a.shown.top, a.shown.follow = top, top == last
}

// page is how far PgUp and PgDn scroll.
func (a *app) page() int {
	return max(a.layout().conversation-2, 1)
}

// pointerDown takes where the pointer's left button went down, which a
// drag selects from and a click acts on once the button comes up.
func (a *app) pointerDown(mouse tea.Mouse) {
	a.unselect()
	a.pressed = nil

	if mouse.Button == tea.MouseLeft {
		a.pressed = &point{x: mouse.X, y: mouse.Y}
	}
}

// drag chooses the text from where the button went down to the pointer,
// within the part of the screen the drag started in. The conversation
// holds still meanwhile, so what is chosen stays under the pointer.
func (a *app) drag(mouse tea.Mouse) {
	if a.pressed == nil || a.floating() {
		return
	}

	within, ok := a.selectable(*a.pressed)

	if !ok {
		return
	}

	to := within.clamp(point{x: mouse.X, y: mouse.Y})

	if a.selection == nil {
		if to == *a.pressed {
			return
		}

		shown := a.layout()

		if a.shown.follow && within.left == shown.columnX {
			a.shown.top, _ = a.conversationTop(shown.column, shown.conversation)
			a.shown.follow, a.resumes = false, true
		}
	}

	a.selection = &selection{from: *a.pressed, to: to, within: within}
}

// selectable is the part of the screen text can be chosen in from a cell:
// the conversation, or the panel's body.
func (a *app) selectable(at point) (area, bool) {
	shown := a.layout()
	conversation := area{left: shown.columnX, top: bodyTop, right: shown.columnX + shown.column, bottom: bodyTop + shown.conversation}

	if !shown.covered && conversation.holds(at) {
		return conversation, true
	}

	panel := area{left: shown.panelX + 1, top: bodyTop + 2, right: a.width, bottom: a.height}

	if shown.panel > 0 && panel.holds(at) {
		return panel, true
	}

	return area{}, false
}

// pointerUp copies what a drag chose, to the clipboard and to the primary
// selection, as a terminal that copies on select does; a press that did
// not drag is a click.
func (a *app) pointerUp() tea.Cmd {
	pressed := a.pressed
	a.pressed = nil

	switch {
	case pressed == nil:
		return nil

	case a.selection != nil:
		text := a.selection.text(a.screenRows(a.layout()))

		if strings.TrimSpace(text) == "" {
			a.unselect()
			return nil
		}

		return tea.Batch(a.copy(text), a.toClipboard(text, true))
	}

	return a.click(tea.Mouse{X: pressed.x, Y: pressed.y, Button: tea.MouseLeft})
}

// unselect lets the selection go, and the conversation follow what comes
// again if it did before the drag.
func (a *app) unselect() {
	if a.selection != nil && a.resumes {
		a.shown.follow = true
	}

	a.selection, a.resumes = nil, false
}
