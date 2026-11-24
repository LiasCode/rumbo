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
	conv     *conversation
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
		described protocol.DescribeResult
		err       error
	}
)

// floating reports whether something floats over the screen.
func (a *app) floating() bool {
	return a.question != nil || a.confirming != nil || a.form != nil || a.palette != nil
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
	case a.palette != nil:
		return a.paletteKey(key, stroke), true
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
	case a.palette != nil:
		for _, r := range line {
			a.palette.query.insert(r)
		}

		a.palette.narrow()
	}
}

// openPalette opens the command palette, asking the provider what it
// offers when that is not known yet.
func (a *app) openPalette() tea.Cmd {
	a.palette = newPalette(a.paletteEntries())

	if a.described == nil {
		return a.describe("")
	}

	return nil
}

// paletteEntries are what the palette offers: the terminal UI's own
// actions, the project's other sessions, the host's functions by what they
// act on, and the provider's.
func (a *app) paletteEntries() []paletteEntry {
	var entries []paletteEntry

	general := func(name, description string, action int) {
		entries = append(entries, paletteEntry{group: "general", name: name, description: description, action: action})
	}

	if a.offers("new") {
		general("New session", "Start the next task in a new session (Ctrl-N)", actionNewSession)
	}

	if a.offers("use") {
		general("Model and options", "Choose the model and the options this session keeps", actionPicker)
	}

	if a.offers("sessions") {
		general("Go to the sessions", "Choose one in the sidebar (Tab)", actionSessions)
	}

	general("Sidebar", "Show or put away the sidebar (Ctrl-B)", actionSidebar)
	general("Copy the last answer", "Put the model's last answer in the clipboard", actionCopy)
	general("Export as Markdown", "Write this session to rumbo-<session>.md in its directory", actionExport)
	general("Help", "Every command and key", actionHelp)

	entries = append(entries, a.sessionEntries()...)

	if a.offers("open") {
		for _, listed := range a.sessions.listed {
			if listed.Value == a.session {
				continue
			}

			title := listed.Label

			if title == "" {
				title = listed.Value
			}

			entries = append(entries, paletteEntry{group: "sessions", name: title, description: listed.Description, action: actionOpen, session: listed.Value})
		}
	}

	for _, function := range commands(a.info.Functions) {
		group := map[string]string{protocol.GroupSystem: "system", protocol.GroupSession: "session", protocol.GroupProvider: "providers"}[function.Group]

		if group == "" {
			group = groupTitle(function.Group)
		}

		entries = append(entries, paletteEntry{group: group, name: function.Name, description: function.Description, action: actionFunction, function: function, source: fromHost})
	}

	provider := "provider " + a.info.Provider

	if a.described == nil {
		return append(entries, paletteEntry{group: provider, name: "asking what it offers…", action: actionNone})
	}

	for _, function := range commands(a.described.Offers.Functions) {
		entries = append(entries, paletteEntry{group: provider, name: function.Name, description: function.Description, action: actionFunction, function: function, source: fromProvider})
	}

	for _, each := range extensionCommands(a.described) {
		entries = append(entries, paletteEntry{group: each.source, name: each.function.Name, description: each.function.Description, action: actionFunction, function: each.function, source: each.source})
	}

	return entries
}

// paletteKey applies a key to the palette: what is typed narrows it, Up and
// Down choose, Enter goes on with the choice, and Escape closes it.
func (a *app) paletteKey(key tea.KeyPressMsg, stroke string) tea.Cmd {
	shown := a.palette

	switch stroke {
	case "esc", "ctrl+c", "ctrl+p":
		a.palette = nil

	case "up", "shift+tab":
		shown.move(-1)

	case "down", "tab":
		shown.move(1)

	case "enter":
		chosen, ok := shown.chosen()

		if !ok || chosen.action == actionNone {
			return nil
		}

		a.palette = nil

		return a.choose(chosen)

	default:
		if !shown.fixed && edit(&shown.query, key, stroke) {
			shown.narrow()
		}
	}

	return nil
}

// choose goes on with what was chosen in the palette.
func (a *app) choose(chosen paletteEntry) tea.Cmd {
	switch chosen.action {
	case actionFunction:
		if len(chosen.function.Params) > 0 {
			return a.openForm(functionForm(chosen.function, chosen.source, a.info.Functions, functionsOf(chosen.source, nil, a.described)))
		}

		return a.run(chosen.source, chosen.function, nil, "/"+chosen.function.Name)

	case actionNewSession:
		a.sessions.choose("")
		return a.open()

	case actionPicker:
		return a.openPicker()

	case actionHelp:
		return a.command(commandHelp)

	case actionCopy:
		return a.command(commandCopy)

	case actionExport:
		return a.command(commandExport)

	case actionSessions:
		a.sidebar = true
		a.focus = focusSidebar
		a.sessions.choose(a.session)

	case actionSidebar:
		a.sidebar = !a.sidebar

	case actionOpen:
		a.sessions.choose(chosen.session)
		return a.open()

	case actionEdit, actionAskAgain:
		return a.branchAt(chosen.point, chosen.text, chosen.images, chosen.action == actionAskAgain)

	case actionCopyText:
		return a.copy(chosen.text)
	}

	return a.sessionAction(chosen.action)
}

// run shows a call of a function as the command it is, and calls it.
func (a *app) run(source string, function protocol.Function, params json.RawMessage, line string) tea.Cmd {
	if a.asking {
		a.notice = "a command is waiting for its answer"
		return nil
	}

	a.shown.add(&entry{kind: entryTyped, text: line})
	a.shown.follow = true

	return a.request(source, function, params)
}

// request calls a function of the host or of the provider, once the person
// agrees when it asks to be confirmed. Its answer shows in the session
// shown.
func (a *app) request(source string, function protocol.Function, params json.RawMessage) tea.Cmd {
	invoke := a.invoke(source, function.Name, params, a.shown, false)

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
	a.palette, a.form = nil, opened

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

// runForm runs the form: the function with its parameters, or the changes
// the picker asks for. What is wrong in it keeps it open, saying why.
func (a *app) runForm() tea.Cmd {
	shown := a.form

	if shown.picking != nil {
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

	params, err := shown.arguments()

	if err != nil {
		shown.problem = err.Error()
		return nil
	}

	a.form = nil

	return a.run(shown.source, shown.function, params, shown.line())
}

// openPicker asks the provider what can be chosen, to open the picker with
// it.
func (a *app) openPicker() tea.Cmd {
	if !a.offers("use") {
		a.notice = "this rumbo cannot change the model"
		return nil
	}

	a.palette = nil
	conn := a.conn

	return func() tea.Msg {
		described, err := conn.Describe()

		return pickerMsg{described, err}
	}
}

// picked opens the picker with what the provider said. A provider that
// could not say still leaves the provider and the model to change.
func (a *app) picked(msg pickerMsg) tea.Cmd {
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

	conn, conv, label := a.conn, a.shown, a.label(fromHost)

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
				return answeredMsg{conv: conv, label: label, text: strings.Join(said, "\n"), err: err, state: state}
			}

			said = append(said, strings.TrimSpace(result.Text))
			state = &result.State
		}

		return answeredMsg{conv: conv, label: label, text: strings.Join(said, "\n"), state: state}
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

	case a.palette != nil && a.palette.fixed:
		border, title = a.theme.progress, a.palette.title
		body = append(a.palette.rows(inner, room-2), "", styleDim+"↑ ↓ choose · Enter does it · Esc closes"+styleReset)

	case a.palette != nil:
		border, title = a.theme.progress, "commands"

		typed, _, column := a.palette.query.view(inner-2, 1, false)
		query := typed[0]

		if a.palette.query.empty() {
			query = styleDim + "type to narrow them" + styleReset
		}

		cursorRow, cursorColumn = 0, 2+column
		body = append([]string{a.theme.progress + "› " + styleColorOff + query, ""}, a.palette.rows(inner, room-4)...)
		body = append(body, "", styleDim+"↑ ↓ choose · Enter runs · Esc closes"+styleReset)

	default:
		return rows, nil
	}

	for index, row := range rows {
		rows[index] = dimmed(row)
	}

	framed := box(title, body, width, border)
	x := max((a.width-width)/2, 0)
	y := max((a.height-len(framed))/2, 1)

	if a.palette != nil && !a.palette.fixed && a.form == nil && a.confirming == nil && a.question == nil {
		y = max(min(a.height/8, a.height-len(framed)), 1)
	}

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
