package tui

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/LiasCode/rumbo/internal/frontends/render"
	"github.com/LiasCode/rumbo/protocol"
)

// Who has the keyboard.
const (
	focusComposer = iota
	focusSidebar
	focusPanel
	focusConversation
)

// tickEvery is how often what works is drawn again, for its spinner and
// its clock.
const tickEvery = 120 * time.Millisecond

// app is the terminal UI's state.
type app struct {
	conn  *protocol.FrontendConn
	info  protocol.FrontendInitializeParams
	theme theme
	// send delivers a message while a request is open, such as each event
	// of a run, in the order they come.
	send func(tea.Msg)
	// clock is what time it is, which tests give; now is when the message
	// being applied came.
	clock func() time.Time
	now   time.Time

	width, height int

	// session is the current session, as the host last said, empty for a
	// new one; shown is its conversation. conversations are those of every
	// session shown or worked in, by id, and runs the runs in progress.
	session       string
	shown         *conversation
	conversations map[string]*conversation
	runs          []*flight
	// unseen are the sessions whose run ended while they were not shown.
	unseen   map[string]bool
	sessions sidebar
	// sidebar is true while the sidebar is shown, and focus says who has
	// the keyboard.
	sidebar bool
	focus   int
	// panels are what shows beside the conversation.
	panels panels

	// input is what is typed in the composer, and pasted the images Ctrl-V
	// put with it, which go with the next task or steer; clipboard reads
	// the image to paste, when a test does not want the desktop's.
	input     inputLine
	pasted    []protocol.Image
	clipboard func(context.Context) ([]byte, error)
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
	// described is what the provider said it offers, once it was asked.
	described *protocol.DescribeResult
	// asking is true while a command waits for its answer.
	asking bool
	// invoking is the id of the provider's function in progress, which
	// Escape gives up, and question what it asked: in which conversation,
	// marked as whose, and in what words; reply is what is typed to answer
	// it.
	invoking      int64
	question      *question
	questionIn    *conversation
	questionLabel string
	questionText  string
	reply         inputLine
	// palette, form, and confirming are what floats over the screen while
	// they are open, after a question.
	palette    *palette
	form       *form
	confirming *confirmation
	// telling counts what was told to the run and has not reached the
	// model yet.
	telling int
	// notice says something until the next key, such as why Enter did
	// nothing.
	notice string
	// unfocused is true while the terminal says its window is not in
	// front, and ticking while a tick is on its way.
	unfocused bool
	ticking   bool
	// pressed is where the pointer's button went down, while it is down;
	// selection is the text chosen by dragging from there, which shows
	// until the next key or click; and resumes says the conversation
	// followed what came before the drag held it still, and goes on doing
	// so once the selection goes.
	pressed   *point
	selection *selection
	resumes   bool
	// afterLoad starts what the command line asked for once the session
	// it continues is shown.
	afterLoad func() tea.Cmd
}

// question is what a provider's function asked the person, and where the
// answer goes. A secret one is not shown while it is typed, nor kept.
type question struct {
	secret bool
	answer chan string
}

// flight is a run this front end started: the id of its request, the
// session it works in, which a run in a new session learns from its first
// event, and the conversation that shows it.
type flight struct {
	id      int64
	session string
	label   string
	conv    *conversation
}

// The messages that come from the host, or from asking it.
type (
	tickMsg     time.Time
	hostGoneMsg struct{}
	runIDMsg    struct {
		run *flight
		id  int64
	}
	eventMsg struct {
		run   *flight
		event protocol.Event
	}
	endedMsg struct {
		run    *flight
		result protocol.RunResult
		err    error
	}
	steeredMsg struct {
		run    *flight
		text   string
		images []protocol.Image
		err    error
	}
	listedMsg struct {
		values []protocol.Value
		err    error
	}
	historyMsg struct {
		conv *conversation
		told protocol.HistoryResult
		err  error
	}
	describedMsg struct {
		described protocol.DescribeResult
		err       error
		// then is a command that waited for the provider to say what it
		// offers.
		then string
	}
	answeredMsg struct {
		conv  *conversation
		label string
		text  string
		err   error
		state *protocol.HostState
		// quiet is true for what the sidebar asked, whose answer the
		// conversation does not show.
		quiet bool
		// then goes on once the answer is taken in, unless it failed, as
		// asking again in a branch does.
		then func() tea.Cmd
	}
	choicesMsg struct {
		menu    *menu
		choices []choice
		hint    string
	}
	invokingMsg struct{ id int64 }
	saidMsg     struct {
		conv  *conversation
		label string
		text  string
	}
	askedMsg struct {
		conv     *conversation
		label    string
		question *question
		text     string
	}
	askGoneMsg struct{ question *question }
	pastedMsg  struct {
		image protocol.Image
		err   error
	}
	recalledMsg struct{ asked []string }
)

func newApp(conn *protocol.FrontendConn, info protocol.FrontendInitializeParams, colors theme) *app {
	a := &app{
		conn:          conn,
		info:          info,
		theme:         colors,
		clock:         time.Now,
		session:       info.Session,
		conversations: map[string]*conversation{},
		unseen:        map[string]bool{},
		history:       &history{},
		sidebar:       true,
	}

	a.now = a.clock()
	a.shown = a.conversationOf(info.Session)
	a.sessions.choose(info.Session)

	return a
}

// conversationOf returns the conversation of a session, made the first time
// it is asked for. A new session's is new each time, and has no history to
// wait for.
func (a *app) conversationOf(session string) *conversation {
	if session == "" {
		fresh := newConversation("", a.label)
		fresh.loaded = true

		return fresh
	}

	if known, ok := a.conversations[session]; ok {
		return known
	}

	made := newConversation(session, a.label)
	a.conversations[session] = made

	return made
}

// label is the mark put on what comes from the host or from the provider,
// saying the source in words as well as in color.
func (a *app) label(source string) string {
	switch source {
	case fromHost:
		return a.theme.host + "[host]" + styleReset
	case fromProvider:
		return a.theme.provider + "[provider " + a.info.Provider + "]" + styleReset
	}

	return ""
}

func (a *app) Init() tea.Cmd {
	conn := a.conn

	watch := func() tea.Msg {
		<-conn.Done()
		return hostGoneMsg{}
	}

	// What was asked before comes back from the host, which keeps it with
	// the sessions. A host that keeps nothing to recall leaves it empty.
	recall := func() tea.Msg {
		recalled, err := conn.Recall()

		if err != nil {
			return nil
		}

		return recalledMsg{recalled.Asked}
	}

	// What the command line asked for starts once the session it continues
	// is shown, so its history comes first.
	begin := func() tea.Cmd {
		switch {
		case a.info.Resume:
			return a.start("(resuming the unfinished run)", protocol.RunParams{Resume: true})
		case a.info.Objective != "":
			return a.start(a.info.Objective, protocol.RunParams{Objective: a.info.Objective})
		}

		return nil
	}

	cmds := []tea.Cmd{watch, recall, a.describe(""), a.list()}

	if a.session != "" {
		a.afterLoad = begin
		a.shown.loading = true
		cmds = append(cmds, a.load(a.shown))
	} else {
		cmds = append(cmds, begin())
	}

	return tea.Batch(cmds...)
}

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	a.now = a.clock()

	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height

	case tea.KeyPressMsg:
		cmd = a.press(msg)

	case tea.PasteMsg:
		if a.floating() {
			a.pasteFloating(msg.Content)
			break
		}

		a.paste(msg.Content)
		cmd = a.refreshMenu()

	case tea.MouseWheelMsg:
		a.unselect()
		a.wheel(msg.Mouse())

	case tea.MouseClickMsg:
		a.pointerDown(msg.Mouse())

	case tea.MouseMotionMsg:
		a.drag(msg.Mouse())

	case tea.MouseReleaseMsg:
		cmd = a.pointerUp()

	case tea.FocusMsg:
		a.unfocused = false

	case tea.BlurMsg:
		a.unfocused = true

	case tickMsg:
		a.ticking = false

	case hostGoneMsg:
		return a, tea.Quit

	case runIDMsg:
		msg.run.id = msg.id

	case eventMsg:
		cmd = a.event(msg.run, msg.event)

	case endedMsg:
		cmd = a.ended(msg)

	case steeredMsg:
		a.steered(msg)

	case listedMsg:
		if msg.err == nil {
			a.sessions.list(msg.values)

			if a.focus != focusSidebar {
				a.sessions.choose(a.session)
			}
		}

	case historyMsg:
		cmd = a.loaded(msg)

	case describedMsg:
		cmd = a.learned(msg)

	case answeredMsg:
		cmd = a.answered(msg)

	case choicesMsg:
		a.offered(msg)

	case invokingMsg:
		a.invoking = msg.id

	case saidMsg:
		msg.conv.add(&entry{kind: entrySaid, label: msg.label, text: msg.text})

	case askedMsg:
		cmd = a.asked(msg)

	case askGoneMsg:
		// What was typed of the answer goes with the question.
		if a.question == msg.question {
			a.question = nil
			a.reply.clear()
		}

	case formListedMsg:
		if a.form == msg.form {
			msg.form.take(msg.field, msg.generation, msg.values, msg.said, msg.err)
		}

	case panelSaidMsg:
		a.panelSaid(msg)

	case changesMsg:
		a.changed(msg)

	case pickerMsg:
		cmd = a.picked(msg)

	case pastedMsg:
		if msg.err != nil {
			a.notice = msg.err.Error()
		} else {
			a.pasted = append(a.pasted, msg.image)
		}

	case recalledMsg:
		a.history.earlier(msg.asked)
	}

	return a, tea.Batch(cmd, a.tick())
}

// tick asks to be told the time again soon while something works, so its
// spinner and its clock move.
func (a *app) tick() tea.Cmd {
	busy := len(a.runs) > 0 || a.shown.working() || a.menu != nil && a.menu.loading

	if a.ticking || !busy {
		return nil
	}

	a.ticking = true

	return tea.Tick(tickEvery, func(now time.Time) tea.Msg { return tickMsg(now) })
}

// leaving is what stays in the terminal once the terminal UI has quit: the
// command that continues its session, or nothing when it has none.
func (a *app) leaving() string {
	if a.session == "" {
		return ""
	}

	return styleDim + "rumbo -s " + a.session + " continues this session" + styleReset
}

// notify tells the person that something they may be waiting for happened
// while the terminal was not in front of them: with the bell, and as a
// desktop notification in the terminals that take OSC 9.
func (a *app) notify(text string) tea.Cmd {
	if !a.unfocused {
		return nil
	}

	plain := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}

		return r
	}, text)

	return tea.Raw("\a" + escape + "]9;" + plain + "\a")
}

// here is the run in the session shown, if one works there.
func (a *app) here() *flight {
	for _, run := range a.runs {
		if run.conv == a.shown {
			return run
		}
	}

	return nil
}

// offers reports whether the host offers a function of its own.
func (a *app) offers(name string) bool {
	return slices.ContainsFunc(a.info.Functions, func(function protocol.Function) bool { return function.Name == name })
}

// submit starts a run on what the person asked, and the images given with
// it.
func (a *app) submit(objective string, images []protocol.Image) tea.Cmd {
	return a.start(objective, protocol.RunParams{Objective: objective, Images: images})
}

// start sends a run to the session shown, which shows label as what it was
// asked, unless one works there already.
func (a *app) start(label string, params protocol.RunParams) tea.Cmd {
	switch {
	case a.here() != nil:
		a.notice = "still working; Escape cancels"
		return nil
	case a.asking:
		a.notice = "a command is waiting for its answer"
		return nil
	case !a.shown.loaded:
		a.notice = "the session is still being read"
		return nil
	}

	params.Session = a.session

	run := &flight{session: a.session, label: label, conv: a.shown}
	a.runs = append(a.runs, run)
	a.shown.ask(label, params.Images, params.Resume, a.now)
	a.shown.follow = true
	a.input.clear()
	a.pasted = nil

	conn, send := a.conn, a.send

	return func() tea.Msg {
		named := func(id int64) { send(runIDMsg{run, id}) }
		shown := func(event protocol.Event) { send(eventMsg{run, event}) }

		result, err := conn.Launch(params, named, shown)

		return endedMsg{run, result, err}
	}
}

// event shows one event of a run, in the conversation of its session. What
// belongs to no run, such as a provider's diagnostics, shows where the
// person is.
func (a *app) event(run *flight, event protocol.Event) tea.Cmd {
	var cmd tea.Cmd

	if run.session == "" && event.Session != "" {
		cmd = a.learn(run, event.Session)
	}

	if event.Run == 0 && event.Type == protocol.EventLog {
		a.shown.apply(event, false, a.now)
		return cmd
	}

	run.conv.apply(event, false, a.now)

	return cmd
}

// learn takes the session a run in a new session works in, which the host
// makes the current one when the run is in the session shown.
func (a *app) learn(run *flight, session string) tea.Cmd {
	run.session = session
	run.conv.session = session

	if _, known := a.conversations[session]; !known {
		a.conversations[session] = run.conv
	}

	if run.conv == a.shown {
		a.session = session
		a.sessions.choose(session)
	}

	return a.list()
}

// ended shows how a run ended. One that ended in a session not shown marks
// it, and says so.
func (a *app) ended(msg endedMsg) tea.Cmd {
	run := msg.run
	a.runs = slices.DeleteFunc(a.runs, func(other *flight) bool { return other == run })

	var cmds []tea.Cmd

	if run.session == "" && msg.result.Session != "" {
		cmds = append(cmds, a.learn(run, msg.result.Session))
	}

	status, reason := msg.result.Status, msg.result.Error

	if msg.err != nil {
		status, reason = protocol.RunFailed, msg.err.Error()
	}

	run.conv.end(status, reason, a.now)

	outcome := status

	if status == protocol.RunFailed && reason != "" {
		outcome += ": " + reason
	}

	if run.conv != a.shown {
		a.unseen[run.session] = true
		a.notice = "the run in “" + a.sessions.title(run.session) + "” ended: " + outcome
	}

	cmds = append(cmds, a.notify("rumbo: "+outcome+" · "+render.FirstLine(run.label)), a.list(), a.readPanel())

	return tea.Batch(cmds...)
}

// steer tells the run in the session shown what was typed, and the images
// given with it. It shows once the model has it, which is where the model
// reads it.
func (a *app) steer(run *flight, text string, images []protocol.Image) tea.Cmd {
	a.input.clear()
	a.pasted = nil
	a.telling++

	conn, id := a.conn, run.id

	return func() tea.Msg {
		err := conn.SteerRun(id, text, images...)

		return steeredMsg{run, text, images, err}
	}
}

// steered shows what was told to a run once it has it. When it never will,
// as when the run ended first, what was said goes back to the composer, to
// be sent as a task.
func (a *app) steered(msg steeredMsg) {
	a.telling--

	switch {
	case msg.err == nil:
		msg.run.conv.add(&entry{kind: entrySteer, text: msg.text, images: msg.images})
	case a.input.empty() && len(a.pasted) == 0:
		a.notice = msg.err.Error() + "; Enter sends it as a new task"
		a.input.set(msg.text)
		a.pasted = msg.images
	default:
		a.notice = msg.err.Error() + ": " + msg.text
	}
}

// list asks the host for the project's sessions, for the sidebar.
func (a *app) list() tea.Cmd {
	if !a.offers("sessions") {
		return nil
	}

	conn := a.conn

	return func() tea.Msg {
		result, err := conn.InvokeHost("sessions", nil)

		return listedMsg{result.Values, err}
	}
}

// load asks for what a session holds, to show it.
func (a *app) load(conv *conversation) tea.Cmd {
	conn, session := a.conn, conv.session

	return func() tea.Msg {
		told, err := conn.History(session)

		return historyMsg{conv, told, err}
	}
}

// loaded shows what a session held, and starts what waited for it.
func (a *app) loaded(msg historyMsg) tea.Cmd {
	if msg.err != nil {
		msg.conv.loaded, msg.conv.loading = true, false
		a.notice = "the session's history could not be read: " + msg.err.Error()
	} else {
		msg.conv.told(msg.told.Events, a.now)
	}

	if begin := a.afterLoad; begin != nil && msg.conv == a.shown {
		a.afterLoad = nil
		return begin()
	}

	return nil
}

// show makes a session the one shown, reading what it holds the first time.
func (a *app) show(session string) tea.Cmd {
	if a.focus == focusConversation {
		a.leaveConversation()
	}

	a.session = session
	a.shown = a.conversationOf(session)
	delete(a.unseen, session)
	a.sessions.choose(session)
	a.sessions.reveal(a.height - bodyTop)

	// The panel shows the session shown.
	read := a.readPanel()

	if a.shown.loaded || a.shown.loading {
		return read
	}

	a.shown.loading = true

	return tea.Batch(read, a.load(a.shown))
}

// adopt takes what the host says the front end is attached to after a
// function of its own: another session, provider, or model.
func (a *app) adopt(state protocol.HostState) tea.Cmd {
	var cmds []tea.Cmd

	// What is known of the provider was for the provider that was in use,
	// and the new one is asked at once, for its commands.
	if state.Provider != a.info.Provider {
		a.described = nil
		cmds = append(cmds, a.describe(""))
	}

	a.info.Provider, a.info.Model, a.info.Dir = state.Provider, state.Model, state.Dir

	// A new session the host made current shows empty, even after another
	// that had not got its id yet.
	moved := state.Session != a.session || state.Session == "" && (a.shown.session != "" || a.here() != nil)

	if moved {
		cmds = append(cmds, a.show(state.Session))
	}

	return tea.Batch(cmds...)
}

// open shows the session selected in the sidebar, or a new one, by asking
// the host to make it current.
func (a *app) open() tea.Cmd {
	a.focus = focusComposer
	chosen := a.sessions.chosen()

	switch {
	case chosen == a.session && (chosen != "" || a.shown.session == "" && a.here() == nil):
		return nil
	case a.asking:
		a.notice = "a command is waiting for its answer"
		return nil
	case chosen == "" && !a.offers("new"), chosen != "" && !a.offers("open"):
		a.notice = "this rumbo cannot change session"
		return nil
	}

	a.asking = true

	if chosen == "" {
		return a.invoke(fromHost, "new", nil, a.shown, true)
	}

	params, _ := json.Marshal(map[string]string{"session": chosen})

	return a.invoke(fromHost, "open", params, a.shown, true)
}

// describe asks what the provider offers, and then runs a command that
// waited for it.
func (a *app) describe(then string) tea.Cmd {
	conn := a.conn

	return func() tea.Msg {
		described, err := conn.Describe()

		return describedMsg{described, err, then}
	}
}

// learned keeps what the provider offers, and rebuilds a menu of commands
// opened before it was known, so the provider's are in it.
func (a *app) learned(msg describedMsg) tea.Cmd {
	if msg.then != "" {
		a.asking = false
	}

	if msg.err != nil {
		if msg.then != "" {
			a.shown.add(&entry{kind: entryAnswer, label: a.label(fromProvider), text: msg.err.Error(), failed: true})
		}

		// The palette says why the provider's functions are not there.
		if a.palette != nil {
			for index, each := range a.palette.entries {
				if each.action == actionNone {
					a.palette.entries[index].name = "not available: " + msg.err.Error()
				}
			}
		}

		return nil
	}

	a.described = &msg.described

	if a.menu != nil && a.menu.head == "" {
		a.menu = nil
	}

	// A palette opened before the provider said what it offers takes its
	// functions in, keeping what was typed in it.
	if a.palette != nil {
		query := a.palette.query
		a.palette = newPalette(a.paletteEntries())
		a.palette.query = query
		a.palette.narrow()
	}

	if msg.then != "" {
		return a.execute(msg.then)
	}

	return a.refreshMenu()
}

// asked shows what a provider's function asks the person, in the
// conversation it was called from and in a question over everything else,
// whose line the answer is typed in.
func (a *app) asked(msg askedMsg) tea.Cmd {
	a.question, a.questionIn, a.questionLabel, a.questionText = msg.question, msg.conv, msg.label, msg.text
	a.reply.clear()
	a.palette, a.form, a.confirming = nil, nil, nil
	msg.conv.add(&entry{kind: entrySaid, label: msg.label, text: msg.text})

	return a.notify("rumbo: the provider asks: " + msg.text)
}

// answer gives what was typed to the question waiting for it. A secret
// answer is not shown, and no answer is kept in the history.
func (a *app) answer() {
	asked := a.question
	text := a.reply.String()
	shown := text

	if asked.secret {
		shown = "(not shown)"
	}

	a.question = nil
	a.reply.clear()
	a.questionIn.add(&entry{kind: entryTyped, text: shown})
	asked.answer <- text
}

// paste puts pasted text in the composer, its line breaks included; none
// of it sends or completes anything, and what is no text is left out.
func (a *app) paste(text string) {
	for _, r := range strings.ReplaceAll(text, "\r\n", "\n") {
		switch {
		case r == '\r' || r == '\n':
			a.input.insert('\n')
		case r == '\t':
			a.input.insert(' ')
		case r >= 0x20 && r != 0x7f:
			a.input.insert(r)
		}
	}

	a.dismissed = false
}
