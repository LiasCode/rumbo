package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// The screen: the sidebar beside the main area, which holds the
// conversation in a column of its own, the menu of a command being typed,
// the composer, and a line that says what the keys do.

const (
	// bodyTop is the first row of the sidebar, the main area, and the
	// panel.
	bodyTop = 0
	// margin is how far the conversation, the menu, and the composer stay
	// from each side of the main area, which marks the entry chosen.
	margin = 2
	// The sidebar is a fifth of the screen within these bounds, and shows
	// only on a screen at least sidebarFrom columns wide.
	sidebarMin  = 24
	sidebarMax  = 34
	sidebarFrom = 80
)

// frame is where each part of the screen goes, on a screen of the app's
// size.
type frame struct {
	// side is how wide the sidebar is, nothing when it is put away; main is
	// where the main area starts, past the sidebar's border, and mainWidth
	// how wide it is.
	side      int
	main      int
	mainWidth int
	// column is how wide the conversation, the menu, and the composer are,
	// and columnX where they start.
	column  int
	columnX int
	// conversation is how many rows the conversation takes.
	conversation int
	menu         []string
	composer     []string
	// composerTop is the screen row the composer starts on, and cursorRow
	// and cursorColumn where the cursor is in it.
	composerTop  int
	cursorRow    int
	cursorColumn int
	// panel is how wide the panel is, nothing when none is shown, and
	// panelX where it starts; covered is true when there is no room for the
	// conversation beside it, which it then takes the place of.
	panel   int
	panelX  int
	covered bool
}

// The panel is two fifths of the main area within these bounds, beside a
// conversation at least panelBeside columns wide.
const (
	panelMin    = 36
	panelMax    = 64
	panelBeside = 44
)

func (a *app) layout() frame {
	width, height := max(a.width, 20), max(a.height, 8)

	var shown frame

	if a.sidebar && width >= sidebarFrom && a.reaches() {
		shown.side = min(max(width/5, sidebarMin), sidebarMax)
		shown.main = shown.side + 1
	}

	room := width - shown.main
	shown.mainWidth = room

	if a.panels.shown != panelNone {
		shown.panel = min(max(room*2/5, panelMin), panelMax)

		if room-shown.panel-1 < panelBeside {
			shown.panel, shown.covered = room, true
		} else {
			shown.mainWidth = room - shown.panel - 1
		}

		shown.panelX = width - shown.panel
	}

	shown.column = shown.mainWidth - 2*margin

	if shown.column < 20 {
		shown.column = shown.mainWidth
	}

	shown.columnX = shown.main + (shown.mainWidth-shown.column)/2
	shown.composer, shown.cursorRow, shown.cursorColumn = a.composer(shown.column, height)

	if a.menu != nil {
		shown.menu = a.menu.rows(shown.column)
	}

	// Over the line that says what the keys do.
	shown.conversation = max(height-bodyTop-len(shown.menu)-len(shown.composer)-1, 1)
	shown.composerTop = bodyTop + shown.conversation + len(shown.menu)

	return shown
}

func (a *app) View() tea.View {
	var view tea.View

	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.ReportFocus = true

	if a.width == 0 || a.height == 0 {
		return view
	}

	shown := a.layout()
	rows := a.screenRows(shown)

	if a.selection != nil {
		rows = a.selection.highlighted(rows)
	}

	// What floats takes the cursor, and else the composer has it, when it
	// shows.
	if a.floating() {
		rows, view.Cursor = a.float(rows)
	} else if a.focus == focusComposer && !shown.covered {
		view.Cursor = tea.NewCursor(shown.columnX+2+shown.cursorColumn, shown.composerTop+shown.cursorRow)
	}

	view.SetContent(strings.Join(rows, "\n"))

	return view
}

// screenRows draws the screen as it is under what floats: the sidebar,
// the main area, and the panel beside each other.
func (a *app) screenRows(shown frame) []string {
	var rows []string

	body := a.height - bodyTop

	var side []string

	if shown.side > 0 {
		side = a.sessions.rows(a.theme, a.project(), shown.side, body, a.marked(), a.focus == focusSidebar, a.now)
	}

	var panel []string

	if shown.panel > 0 {
		panel = a.panels.panelRows(a.theme, a.details(), shown.panel, body, a.focus == focusPanel)
	}

	for index, row := range a.main(shown, body) {
		switch {
		case shown.covered:
			row = fit(panel[index], shown.panel)
		case shown.panel > 0:
			row += styleDim + "│" + styleReset + fit(panel[index], shown.panel)
		}

		if shown.side > 0 {
			row = fit(side[index], shown.side) + styleDim + "│" + styleReset + row
		}

		rows = append(rows, row)
	}

	return rows
}

// fit is a row of exactly width columns: cut when it is longer, and filled
// when it is shorter.
func fit(row string, width int) string {
	return pad(cut(row, width), width) + styleReset
}

// project is what the project is called: its folder's name.
func (a *app) project() string {
	return filepath.Base(a.info.Dir)
}

// titleOf is what the session of a rumbo is called: its title, or its id
// when it has none.
func titleOf(held *host) string {
	switch {
	case held.session == "":
		return "new session"
	case held.called == "":
		return held.session
	}

	return held.called
}

// spread puts left and right on one row of width columns, apart, leaving
// out right when both do not fit.
func spread(left, right string, width int) string {
	gap := width - visibleWidth(left) - visibleWidth(right)

	if gap < 1 {
		return fit(left, width)
	}

	return left + styleReset + strings.Repeat(" ", gap) + right + styleReset
}

// main is the main area, body rows high: the conversation, the menu, the
// composer, and the line that says what the keys do, in the column.
func (a *app) main(shown frame, body int) []string {
	conversation, chosen := a.conversationRows(shown.column, shown.conversation)
	column := conversation

	for len(column) < shown.conversation {
		column = append(column, "")
	}

	column = append(column, shown.menu...)
	column = append(column, shown.composer...)

	top, all := a.conversationTop(shown.column, shown.conversation)
	column = append(column, a.hint(shown.column, top+shown.conversation < len(all)))

	gap := shown.columnX - shown.main
	margin := strings.Repeat(" ", gap)

	// The entry chosen in the conversation is marked in the margin.
	marked := margin

	if gap >= 2 {
		marked = strings.Repeat(" ", gap-2) + a.theme.progress + "▌" + styleColorOff + " "
	}

	rows := make([]string, body)

	for index := range rows {
		row, lead := "", margin

		if index < len(column) {
			row = column[index]
		}

		if index < len(chosen) && chosen[index] {
			lead = marked
		}

		rows[index] = fit(lead+fit(row, shown.column), shown.mainWidth)
	}

	return rows
}

// conversationTop is the first row of the conversation shown in height
// rows of width columns: where its end shows, while it follows what comes,
// or where it was scrolled to. It returns the rows too.
func (a *app) conversationTop(width, height int) (int, []string) {
	shown := a.shown
	all := shown.rows(a.theme, width, a.now)
	last := max(len(all)-height, 0)
	top := shown.top

	if shown.follow || top > last {
		top = last
	}

	return top, all
}

// conversationRows is the part of the conversation shown in height rows,
// and which of them the entry chosen is drawn on, while the conversation
// has the keys.
func (a *app) conversationRows(width, height int) ([]string, []bool) {
	top, all := a.conversationTop(width, height)

	if len(all) == 0 {
		return a.welcome(width, height), nil
	}

	visible := all[top:min(top+height, len(all))]
	chosen := make([]bool, len(visible))

	if drawn, ok := a.shown.spanOf(a.shown.selected); ok && a.focus == focusConversation {
		for row := max(drawn.from, top); row < min(drawn.to, top+len(visible)); row++ {
			chosen[row-top] = true
		}
	}

	return visible, chosen
}

// welcome is what an empty conversation shows: what it is waiting for.
func (a *app) welcome(width, height int) []string {
	lines := []string{styleBold + "rumbo" + styleReset, "", styleDim + "Give it a task below, or a command after a slash." + styleReset, ""}

	// The few commands a person needs first, lined up.
	for _, each := range [][2]string{
		{"/model", "choose the model"},
		{"/resume", "go on in another session"},
		{"/providers", "set up what reaches the model"},
		{"/help", "every command and key"},
	} {
		lines = append(lines, pad(each[0], 12)+styleDim+pad(each[1], 30)+styleReset)
	}

	if !a.shown.loaded {
		lines = []string{styleDim + "reading the session…" + styleReset}
	}

	rows := make([]string, max((height-len(lines))/2, 0))

	for _, line := range lines {
		rows = append(rows, strings.Repeat(" ", max((width-visibleWidth(line))/2, 0))+line)
	}

	return rows
}

// composer draws the composer width columns wide on a screen height rows
// high: what is typed in a box, the images pasted over it, and under it
// the model, how full its context is, what the session spent, and how the
// run here stands. It returns where the cursor is in it.
func (a *app) composer(width, height int) ([]string, int, int) {
	inner := max(width-4, 1)
	typed, row, column := a.input.view(inner, max(min(10, height/3), 1), false)

	border := styleDim

	if a.here() != nil {
		border = a.theme.progress
	}

	if a.input.empty() {
		typed = []string{styleDim + a.placeholder() + styleReset}
	}

	// What a command still takes shows after the cursor, dimmed, while it
	// fits.
	if ghost := a.ghost(); ghost != "" && row == len(typed)-1 && visibleWidth(typed[row])+visibleWidth(ghost) <= inner {
		typed[row] += styleDim + ghost + styleReset
	}

	line := func(text string) string {
		return border + "│" + styleReset + " " + fit(text, inner) + " " + border + "│" + styleReset
	}

	rows := []string{border + "╭" + strings.Repeat("─", inner+2) + "╮" + styleReset}

	if len(a.pasted) > 0 {
		rows = append(rows, line(styleDim+imageLabels(a.pasted)+"  Backspace on an empty line takes the last away"+styleReset))
		row++
	}

	for _, text := range typed {
		rows = append(rows, line(text))
	}

	// On a tall screen, the composer has room to breathe between what is
	// typed and what is said under it.
	if height >= 30 {
		rows = append(rows, line(""))
	}

	rows = append(rows, line(a.footer(inner)))
	rows = append(rows, border+"╰"+strings.Repeat("─", inner+2)+"╯"+styleReset)

	return rows, row + 1, column
}

// ghost is what the command being typed still takes, to show after the
// cursor: nothing unless the cursor is at the end of a one-line command,
// after its name or a space.
func (a *app) ghost() string {
	line := a.input.String()

	if a.searching || !a.input.atEnd() || !isCommandStart(line) || line == "/" {
		return ""
	}

	words := strings.Fields(line)
	spaced := strings.HasSuffix(line, " ")

	if !spaced && len(words) > 1 {
		return ""
	}

	still := ""

	if each, found := verbNamed(words[0]); found {
		still = each.still(words[1:])
	} else if function, _, found := lookup(words[0], a.info.Functions); found {
		_, pending := typedParams(function, words[1:])

		var parts []string

		for _, param := range pending {
			parts = append(parts, signature(param))
		}

		still = strings.Join(parts, " ")
	} else {
		return ""
	}

	if still == "" || spaced {
		return still
	}

	return " " + still
}

// placeholder says what the composer takes while it is empty.
func (a *app) placeholder() string {
	if a.here() != nil {
		return "Tell the run something; it reads it before its next step"
	}

	return "Give rumbo a task"
}

// chip is the model the session uses, at the start of the composer's last
// row, which a click opens the picker on.
func (a *app) chip() string {
	// A session can start before its model is chosen, which the picker
	// does.
	if a.info.Model == "" {
		return styleBold + a.info.Provider + styleWeightOff + " " + a.theme.warning + "no model; click here or type /model to choose one" + styleColorOff
	}

	return styleBold + a.info.Provider + styleWeightOff + styleDim + " · " + styleWeightOff + a.info.Model
}

// where is the Git branch checked out and the session's directory, under
// the home directory as ~/, beside the model.
func (a *app) where() (string, string) {
	branch := ""

	if a.branch != "" {
		branch = styleDim + "⎇ " + styleWeightOff + a.branch
	}

	return branch, styleDim + homed(a.info.Dir) + styleWeightOff
}

// homed is dir with the person's home directory at its start written ~.
func homed(dir string) string {
	home, err := os.UserHomeDir()

	if err != nil || home == "" || home == "/" {
		return dir
	}

	if dir == home {
		return "~"
	}

	if rest, under := strings.CutPrefix(dir, home+string(filepath.Separator)); under {
		return "~" + string(filepath.Separator) + rest
	}

	return dir
}

// footer is the composer's last row: the model, the branch and the
// directory, how full its context is, and what the session spent on the
// left, and on the right how the run here stands, or how many work in
// other sessions. What does not fit goes,
// the least needed first.
func (a *app) footer(width int) string {
	model := a.chip()
	branch, dir := a.where()
	context, window := a.shown.usage.Fill()
	meter := contextMeter(a.theme, context, window)
	spent := spentOf(a.shown.usage)
	state := ""

	switch run := a.here(); {
	case run != nil:
		state = a.theme.progress + spin(a.now) + styleColorOff + " working " + elapsed(a.now.Sub(run.conv.run.started))
	case a.asking:
		state = styleDim + "waiting for the answer" + styleWeightOff
	case a.elsewhere() > 0:
		state = a.theme.progress + spin(a.now) + styleColorOff + fmt.Sprintf(" %d working elsewhere", a.elsewhere())
	}

	for _, left := range [][]string{
		{model, branch, dir, meter, spent},
		{model, branch, dir, meter},
		{model, branch, meter},
		{model, branch},
		{model},
	} {
		if shown := together(left); visibleWidth(shown)+visibleWidth(state)+2 <= width || len(left) == 1 {
			return spread(shown, state, width)
		}
	}

	return ""
}

// hint is the line under the composer: what was just said, or what the
// keys do now, and on the right whether there is more of the conversation
// below what shows.
func (a *app) hint(width int, below bool) string {
	keys := styleDim + "Enter sends · / commands · Shift-Tab messages · Shift-Enter new line" + styleWeightOff

	// Tab goes to the sessions while the sidebar shows them.
	switch {
	case a.reaches() && a.layout().side > 0:
		keys = styleDim + "Enter sends · / commands · Tab sessions · Shift-Tab messages · Shift-Enter new line" + styleWeightOff
	case a.reaches():
		keys = styleDim + "Enter sends · / commands · ^B sessions · Shift-Tab messages · Shift-Enter new line" + styleWeightOff
	}

	switch {
	case a.notice != "":
		keys = a.theme.warning + a.notice + styleColorOff
	case a.searching && a.found < 0 && a.query != "":
		keys = "search: " + a.query + styleDim + "  (nothing holds it)" + styleWeightOff
	case a.searching:
		keys = "search: " + a.query + styleDim + "  (Ctrl-R older, Enter takes it, Escape leaves)" + styleWeightOff
	case a.focus == focusSidebar:
		keys = styleDim + "j k choose · gg G ends · Enter shows it · d deletes · Tab goes on" + styleWeightOff
	case a.focus == focusPanel:
		keys = styleDim + "↑ ↓ PgUp PgDn scroll the panel · Esc puts it away · Tab goes back" + styleWeightOff
	case a.focus == focusConversation:
		keys = styleDim + "j k choose · J K your messages · gg G ends · Enter " + a.acting() + " · c copies · b branches · Esc goes back" + styleWeightOff
	case a.menu != nil:
		keys = styleDim + "↑ ↓ choose · Tab takes · Enter sends · Esc closes" + styleWeightOff
	case a.here() != nil && a.telling > 0:
		keys = styleDim + fmt.Sprintf("%d message(s) wait for the run's next turn · Esc cancels", a.telling) + styleWeightOff
	case a.here() != nil:
		keys = styleDim + "Enter tells the run · Esc cancels" + styleWeightOff
	}

	more := ""

	if below {
		more = styleDim + "more below · PgDn" + styleWeightOff
	}

	return spread(" "+keys, more, width)
}

// acting says what Enter does with the entry chosen in the conversation.
func (a *app) acting() string {
	if a.shown.selected < 0 || a.shown.selected >= len(a.shown.entries) {
		return "acts on it"
	}

	switch chosen := a.shown.entries[a.shown.selected]; {
	case chosen.kind == entryTool && chosen.expanded:
		return "shows less of it"
	case chosen.kind == entryTool && chosen.finished:
		return "shows all of it"
	case chosen.kind == entryTask && a.branches():
		return "changes it or asks it again"
	}

	return "copies it"
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
