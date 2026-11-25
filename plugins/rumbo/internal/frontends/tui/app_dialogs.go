package tui

import (
	"encoding/json"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/LiasCode/rumbo/protocol"
)

// What floats over the screen and takes the keyboard while it is open:
// the command palette, a form, a confirmation, or a question of a
// provider's function, which comes before the rest. Only one is open at a
// time.

// confirmation asks the person to agree to a function that cannot be
// undone, with the words its description gives, before it is called.
type confirmation struct {
	function protocol.Function
	run      tea.Cmd
	// act, when there is one, is what agreeing does in place of run: what
	// asks the host itself, once agreed.
	act  func() tea.Cmd
	conv *conversation
	// agree is true while the button that goes ahead has the keyboard.
	agree bool
}

// The messages of what floats.
type (
	// formListedMsg is a list a form asked for one of its fields.
	formListedMsg struct {
		form       *form
		field      int
		generation int
		values     []protocol.Value
		said       string
		err        error
	}
	// pickerMsg is what the provider said it offers, to open the picker
	// with.
	pickerMsg struct {
		host      *host
		described protocol.DescribeResult
		err       error
	}
)

// floating reports whether something floats over the screen.
func (a *app) floating() bool {
	return a.question != nil || a.confirming != nil || a.form != nil || a.actions != nil
}

// floatingKey applies a key to what floats, and reports whether something
// did.
func (a *app) floatingKey(key tea.KeyPressMsg, stroke string) (tea.Cmd, bool) {
	switch {
	case a.question != nil:
		return a.questionKey(key, stroke), true
	case a.confirming != nil:
		return a.confirmKey(stroke), true
	case a.form != nil:
		return a.formKey(key, stroke), true
	case a.actions != nil:
		return a.actionsKey(stroke), true
	}

	return nil, false
}

// pasteFloating puts pasted text in the line of what floats, on one line.
func (a *app) pasteFloating(text string) {
	line := strings.Join(strings.Fields(text), " ")

	switch {
	case a.question != nil:
		for _, r := range line {
			a.reply.insert(r)
		}
	case a.form != nil && len(a.form.fields) > 0:
		focused := a.form.fields[a.form.focus]

		for _, r := range line {
			focused.value.insert(r)
		}

		focused.edited()
	}
}

// actionsKey applies a key to the menu of what can be done with a message:
// Up and Down choose, Enter does it, and Escape closes it.
func (a *app) actionsKey(stroke string) tea.Cmd {
	shown := a.actions

	switch stroke {
	case "esc", "ctrl+c":
		a.actions = nil

	case "up", "shift+tab", "k":
		shown.move(-1)

	case "down", "tab", "j":
		shown.move(1)

	case "enter":
		chosen, ok := shown.chosen()

		if !ok {
			return nil
		}

		a.actions = nil

		switch chosen.action {
		case actionEdit, actionAskAgain:
			return a.branchAt(chosen.point, chosen.text, chosen.files, chosen.action == actionAskAgain)
		case actionCopyText:
			return a.copy(chosen.text)
		}
	}

	return nil
}

// request calls a function of the host or of the provider, once the person
// agrees when it asks to be confirmed. Its answer shows in the session
// shown.
func (a *app) request(source string, function protocol.Function, params json.RawMessage) tea.Cmd {
	invoke := a.invoke(source, function.Name, params, a.shown)

	if function.Confirm != "" {
		a.confirming = &confirmation{function: function, run: invoke, conv: a.shown}
		return nil
	}

	a.asking = true

	return invoke
}

// confirmKey applies a key to a confirmation: Left, Right, and Tab go
// between its buttons, Enter presses the one that has the keyboard, which
// is the one that leaves things as they are until another is chosen, and
// Escape leaves them so.
func (a *app) confirmKey(stroke string) tea.Cmd {
	asked := a.confirming

	switch stroke {
	case "left", "right", "tab", "shift+tab":
		asked.agree = !asked.agree

	case "enter":
		a.confirming = nil

		if !asked.agree {
			asked.conv.add(&entry{kind: entryAnswer, text: "nothing was done"})
			return nil
		}

		if asked.act != nil {
			return asked.act()
		}

		if a.asking {
			a.notice = "a command is waiting for its answer"
			return nil
		}

		a.asking = true

		return asked.run

	case "esc", "ctrl+c":
		a.confirming = nil
		asked.conv.add(&entry{kind: entryAnswer, text: "nothing was done"})
	}

	return nil
}

// openForm opens a form, and asks for the lists its fields offer.
func (a *app) openForm(opened *form) tea.Cmd {
	a.actions, a.form = nil, opened

	return a.lists(opened)
}

// lists asks for the lists a form needs now.
func (a *app) lists(asking *form) tea.Cmd {
	var cmds []tea.Cmd

	for _, each := range asking.wanted() {
		conn := a.conn

		cmds = append(cmds, func() tea.Msg {
			listed := formListedMsg{form: asking, field: each.field, generation: each.generation}

			listed.values, listed.said, listed.err = listFrom(conn, each.target, each.function, each.params)

			return listed
		})
	}

	return tea.Batch(cmds...)
}

// formKey applies a key to a form: Tab and Shift-Tab go between its
// fields, Up and Down put a value of the list in the field, or go between
// fields without one, what is typed edits the field and narrows its list,
// Enter runs it, and Escape closes it.
func (a *app) formKey(key tea.KeyPressMsg, stroke string) tea.Cmd {
	shown := a.form

	if len(shown.fields) == 0 {
		a.form = nil
		return nil
	}

	focused := shown.fields[shown.focus]

	switch stroke {
	case "esc", "ctrl+c":
		a.form = nil

	case "tab":
		return a.focusField(shown.focus + 1)

	case "shift+tab":
		return a.focusField(shown.focus - 1)

	case "up":
		if !focused.move(-1) {
			return a.focusField(shown.focus - 1)
		}

	case "down":
		if !focused.move(1) {
			return a.focusField(shown.focus + 1)
		}

	case "enter":
		return a.runForm()

	default:
		before := focused.value.String()

		if edit(&focused.value, key, stroke) && focused.value.String() != before {
			focused.edited()
			shown.problem = ""
		}
	}

	return nil
}

// focusField gives the keyboard to another field of the form, and asks for
// the lists what was given in the one left changes.
func (a *app) focusField(index int) tea.Cmd {
	shown := a.form
	index = max(min(index, len(shown.fields)-1), 0)

	if index == shown.focus {
		return nil
	}

	shown.focus = index

	return a.lists(shown)
}

// runForm asks for the changes the picker asks for. What is wrong in it
// keeps it open, saying why.
func (a *app) runForm() tea.Cmd {
	shown := a.form
	changes, err := shown.changes()

	if err != nil {
		shown.problem = err.Error()
		return nil
	}

	a.form = nil

	if len(changes) == 0 {
		a.notice = "nothing changed"
		return nil
	}

	return a.change(changes)
}

// openPicker asks the provider what can be chosen, to open the picker with
// it.
func (a *app) openPicker() tea.Cmd {
	if !a.offers("use") {
		a.notice = "this rumbo cannot change the model"
		return nil
	}

	held := a.host

	return func() tea.Msg {
		described, err := held.conn.Describe()

		return pickerMsg{held, described, err}
	}
}

// picked opens the picker with what the provider said, unless the person
// went to another session meanwhile. A provider that could not say still
// leaves the provider and the model to change.
func (a *app) picked(msg pickerMsg) tea.Cmd {
	if msg.host != a.host {
		return nil
	}

	described := msg.described

	if msg.err == nil {
		a.described = &described
	} else {
		described = protocol.DescribeResult{Provider: a.info.Provider, Model: a.info.Model}
	}

	index := slices.IndexFunc(a.info.Functions, func(function protocol.Function) bool { return function.Name == "use" })

	if index < 0 {
		return nil
	}

	opened := pickerForm(a.info.Functions[index], a.offers("session-set"), described, a.info.Functions)

	if msg.err != nil {
		opened.problem = described.Provider + " could not say what it offers: " + msg.err.Error()
	}

	return a.openForm(opened)
}

// change asks the host for the changes the picker asks for, one after the
// other, each shown as the command it is, and stops at the first refused.
func (a *app) change(changes []pickChange) tea.Cmd {
	if a.asking {
		a.notice = "a command is waiting for its answer"
		return nil
	}

	for _, each := range changes {
		a.shown.add(&entry{kind: entryTyped, text: each.line()})
	}

	a.shown.follow = true
	a.asking = true

	held, conv, label := a.host, a.shown, a.label(fromHost)
	conn := held.conn

	return func() tea.Msg {
		var said []string
		var state *protocol.HostState

		for _, each := range changes {
			name, params := "use", each.use

			if each.option != "" {
				name = "session-set"
				params, _ = json.Marshal(map[string]string{"change": each.option})
			}

			result, err := conn.InvokeHost(name, params)

			if err != nil {
				return answeredMsg{host: held, conv: conv, label: label, text: strings.Join(said, "\n"), err: err, state: state}
			}

			said = append(said, strings.TrimSpace(result.Text))
			state = &result.State
		}

		return answeredMsg{host: held, conv: conv, label: label, text: strings.Join(said, "\n"), state: state}
	}
}

// questionKey applies a key to a question: Enter sends what is typed, and
// Escape gives up the function that asked, or declines to answer a tool.
func (a *app) questionKey(key tea.KeyPressMsg, stroke string) tea.Cmd {
	switch stroke {
	case "enter":
		a.answer()

	case "esc", "ctrl+c":
		if a.question.declined != nil {
			a.decline()
		} else {
			a.cancel()
		}

	default:
		edit(&a.reply, key, stroke)
	}

	return nil
}

// float draws what floats over rows, the screen, which shows dimmed under
// it, and returns where the cursor is, if it shows.
func (a *app) float(rows []string) ([]string, *tea.Cursor) {
	width := max(min(84, a.width-4), 20)
	inner := width - 4
	room := max(a.height-8, 3)
	border, title := styleDim, ""
	cursorRow, cursorColumn := -1, 0

	var body []string

	switch {
	case a.question != nil:
		border, title = a.theme.warning, a.questionFrom+" asks"
		body = append(wrapAll(a.questionLabel+" "+a.questionText, inner), "")

		typed, _, column := a.reply.view(inner-2, 1, a.question.secret)
		cursorRow, cursorColumn = len(body), 2+column
		body = append(body, a.theme.warning+"› "+styleColorOff+typed[0], "")

		hint := "Enter sends the answer · Esc gives up"

		if a.question.declined != nil {
			hint = "Enter sends the answer · Esc declines"
		}

		if a.question.secret {
			hint = "the answer is not shown, nor kept · " + hint
		}

		body = append(body, styleDim+hint+styleReset)

	case a.confirming != nil:
		border, title = a.theme.failure, a.confirming.function.Name
		body = append(wrapAll(a.confirming.function.Confirm, inner), "")

		leave, agree := " Cancel ", " "+capitalized(a.confirming.function.Name)+" "

		if a.confirming.agree {
			agree = styleReverse + a.theme.failure + agree + styleReset
		} else {
			leave = styleReverse + leave + styleReset
		}

		body = append(body, leave+"   "+agree, "", styleDim+"← → choose · Enter presses · Esc leaves things as they are"+styleReset)

	case a.form != nil:
		border, title = a.theme.progress, a.form.title

		var row, column int

		body, row, column = a.form.rows(a.theme, inner, room-2)
		cursorRow, cursorColumn = row, column
		body = append(body, "", styleDim+"Tab next field · ↑ ↓ a value of the list · Enter runs · Esc closes"+styleReset)

	case a.actions != nil:
		border, title = a.theme.progress, a.actions.title
		body = append(a.actions.rows(inner, room-2), "", styleDim+"↑ ↓ choose · Enter does it · Esc closes"+styleReset)

	default:
		return rows, nil
	}

	for index, row := range rows {
		rows[index] = dimmed(row)
	}

	framed := box(title, body, width, border)
	x := max((a.width-width)/2, 0)
	y := max((a.height-len(framed))/2, 1)

	rows = lay(rows, framed, x, y)

	if cursorRow < 0 {
		return rows, nil
	}

	return rows, tea.NewCursor(x+2+cursorColumn, y+1+cursorRow)
}

// capitalized is a name with its first letter in capitals, as a button
// says it.
func capitalized(name string) string {
	runes := []rune(name)

	if len(runes) == 0 {
		return name
	}

	return strings.ToUpper(string(runes[:1])) + string(runes[1:])
}
