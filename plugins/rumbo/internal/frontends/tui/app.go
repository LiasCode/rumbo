package tui

import (
	"context"
	"errors"
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
	theme theme
	// send delivers a message while a request is open, such as each event
	// of a run, in the order they come.
	send func(tea.Msg)
	// clock is what time it is, which tests give; now is when the message
	// being applied came.
	clock func() time.Time
	now   time.Time

	width, height int

	// host is the rumbo whose session shows, and hosts every one the
	// terminal UI holds, the one that started it first. What is said here
	// of the session, its conversation, and its run is the one shown's.
	*host
	hosts []*host
	// rumbos starts the rumbos of the other sessions and lists them, and
	// opening is true while one starts.
	rumbos  rumbos
	opening bool
	// sessions lists the sessions of the directory beside the
	// conversation, while sidebar is true and there is room; focus says
	// who has the keyboard.
	sessions sidebar
	sidebar  bool
	focus    int
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
	questionFrom  string
	reply         inputLine
	// palette, form, and confirming are what floats over the screen while
	// they are open, after a question.
	palette    *palette
	form       *form
	confirming *confirmation
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
}

// question is what a function, or a tool of a run, asked the person, and
// where the answer goes. A secret one is not shown while it is typed, nor
// kept. A tool's can be declined, which leaves the tool to decide what that
// means and the run going; a function's is given up with the function.
type question struct {
	secret   bool
	answer   chan string
	declined chan struct{}
}

// flight is a run this front end started: the rumbo it works in, the id of
// its request there, what it was asked, and the conversation that shows
// it.
type flight struct {
	host  *host
	id    int64
	label string
	conv  *conversation
}

// The messages that come from the host, or from asking it.
type (
	tickMsg     time.Time
	hostGoneMsg struct{ host *host }
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
	historyMsg struct {
		host *host
		told protocol.HistoryResult
		err  error
	}
	describedMsg struct {
		host      *host
		described protocol.DescribeResult
		err       error
		// then is a command that waited for the provider to say what it
		// offers.
		then string
	}
	answeredMsg struct {
		// host is the rumbo that answered.
		host  *host
		conv  *conversation
		label string
		text  string
		err   error
		state *protocol.HostState
		// stale is true for the answer of an extension's function, which
		// may have changed what the extension offers, as a sign-in does.
		stale bool
		// opens is the session the answer made, a branch, which shows
		// next, and then goes on once it does.
		opens string
		then  func() tea.Cmd
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
		// who asks, in words: the provider, an extension, or a tool.
		who string
	}
	// toolAskedMsg is what a tool asked during a run of a rumbo.
	toolAskedMsg struct {
		host     *host
		asked    protocol.FrontendAskParams
		question *question
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
		theme:   colors,
		clock:   time.Now,
		history: &history{},
		sidebar: true,
	}

	a.now = a.clock()
	a.host = a.hold(conn, info, nil)
	a.sessions.choose(info.Session)

	return a
}

// label is the mark put on what comes from the host, from the provider, or
// from an extension of the session shown.
func (a *app) label(source string) string {
	return a.labelOf(a.host, source)
}

// labelOf is the mark put on what comes from a host, from its provider, or
// from an extension, saying the source in words as well as in color. An
// extension is the user's own, as the provider is, and has its color.
func (a *app) labelOf(held *host, source string) string {
	switch source {
	case fromHost:
		return a.theme.host + "[host]" + styleReset
	case fromProvider:
		return a.theme.provider + "[provider " + held.info.Provider + "]" + styleReset
	case fromExtensions:
		return a.theme.provider + "[extension]" + styleReset
	}

	if name, named := extensionOf(source); named {
		return a.theme.provider + "[extension " + name + "]" + styleReset
	}

	return ""
}

func (a *app) Init() tea.Cmd {
	conn := a.conn

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

	cmds := []tea.Cmd{recall, a.list()}

	if a.session != "" {
		a.afterLoad = begin
	} else {
		cmds = append(cmds, begin())
	}

	return tea.Batch(append(cmds, a.attach(a.host))...)
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
		cmd = a.gone(msg.host)

	case listedMsg:
		a.listed(msg)

	case openedMsg:
		cmd = a.opened(msg)

	case rumbosMsg:
		a.asking = false
		msg.conv.add(&entry{kind: entryAnswer, text: a.rumbosText(msg.said)})

	case runIDMsg:
		msg.run.id = msg.id

	case eventMsg:
		cmd = a.event(msg.run, msg.event)

	case endedMsg:
		cmd = a.ended(msg)

	case steeredMsg:
		a.steered(msg)

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

	case toolAskedMsg:
		who := "the tool " + msg.asked.Name

		// One asked in a session that does not show says which.
		if msg.host != a.host {
			who += " in “" + titleOf(msg.host) + "”"
		}

		cmd = a.asked(askedMsg{
			conv:     msg.host.shown,
			label:    a.theme.provider + "[tool " + msg.asked.Name + "]" + styleReset,
			question: msg.question,
			text:     msg.asked.Text,
			who:      who,
		})

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
	busy := len(a.runs) > 0 || a.elsewhere() > 0 || a.shown.working() || a.menu != nil && a.menu.loading

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

// here is the run in progress in the session shown, if one works.
func (a *app) here() *flight {
	if len(a.runs) == 0 {
		return nil
	}

	return a.runs[0]
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

// start sends a run to the rumbo shown, and shows label as what it was
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

	run := &flight{host: a.host, label: label, conv: a.shown}
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

// event shows one event of a run. The first of a new session tells the id
// the session got.
func (a *app) event(run *flight, event protocol.Event) tea.Cmd {
	var cmd tea.Cmd

	if event.Session != "" {
		cmd = a.learn(run.host, event.Session, run.label)
	}

	run.conv.apply(event, false, a.now)

	return cmd
}

// learn takes the id a new session got when its first run made it, which
// the sidebar lists from then on. It is called what it was first asked,
// which the host's next answer says for itself.
func (a *app) learn(held *host, session, asked string) tea.Cmd {
	if held.session != "" {
		return nil
	}

	held.session, held.shown.session = session, session
	held.called = render.FirstLine(asked)

	if held == a.host {
		a.sessions.choose(session)
	}

	return a.list()
}

// ended shows how a run ended. One that ended in a session not shown marks
// it, and says so.
func (a *app) ended(msg endedMsg) tea.Cmd {
	run := msg.run
	run.host.runs = slices.DeleteFunc(run.host.runs, func(other *flight) bool { return other == run })

	var cmds []tea.Cmd

	if msg.result.Session != "" {
		cmds = append(cmds, a.learn(run.host, msg.result.Session, run.label))
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

	// A rumbo left behind has nothing to do now, and may be one too many.
	if run.host != a.host && slices.Contains(a.hosts, run.host) {
		run.host.unseen = true
		a.notice = "the run in “" + titleOf(run.host) + "” ended: " + outcome
		cmds = append(cmds, a.tidy(keptIdle))
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
	run.host.telling++

	conn, id := run.host.conn, run.id

	return func() tea.Msg {
		err := conn.SteerRun(id, text, images...)

		return steeredMsg{run, text, images, err}
	}
}

// steered shows what was told to a run once it has it. When it never will,
// as when the run ended first, what was said goes back to the composer, to
// be sent as a task.
func (a *app) steered(msg steeredMsg) {
	msg.run.host.telling--

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

// load asks a rumbo for what its session holds, to show it.
func (a *app) load(held *host) tea.Cmd {
	conn := held.conn

	return func() tea.Msg {
		told, err := conn.History()

		return historyMsg{held, told, err}
	}
}

// loaded shows what a session held, and starts what waited for it, unless
// the person went to another meanwhile.
func (a *app) loaded(msg historyMsg) tea.Cmd {
	held := msg.host

	if msg.err != nil {
		held.shown.loaded, held.shown.loading = true, false
		a.notice = "the session's history could not be read: " + msg.err.Error()
	} else {
		held.shown.told(msg.told.Events, a.now)
	}

	begin := held.afterLoad
	held.afterLoad = nil

	if begin != nil && held == a.host {
		return begin()
	}

	return nil
}

// adopt takes what a host says the front end is attached to after a
// function of its own: another provider or model, or what the session is
// called.
func (a *app) adopt(held *host, state protocol.HostState) tea.Cmd {
	var cmd tea.Cmd

	// What is known of the provider was for the provider that was in use,
	// and the new one is asked at once, for its commands.
	if state.Provider != held.info.Provider {
		held.described = nil
		cmd = a.describe(held, "")
	}

	held.info.Provider, held.info.Model, held.info.Dir = state.Provider, state.Model, state.Dir

	// A new session has its id from its first run on, and any is called
	// what the host says, which a command may have changed.
	if state.Session != "" {
		held.session, held.shown.session, held.called = state.Session, state.Session, state.Title
	}

	return cmd
}

// describe asks a rumbo what its provider offers, and then runs a command
// that waited for it.
func (a *app) describe(held *host, then string) tea.Cmd {
	conn := held.conn

	return func() tea.Msg {
		described, err := conn.Describe()

		return describedMsg{held, described, err, then}
	}
}

// learned keeps what the provider offers, and rebuilds a menu of commands
// opened before it was known, so the provider's are in it.
func (a *app) learned(msg describedMsg) tea.Cmd {
	if msg.then != "" {
		a.asking = false
	}

	// What a rumbo that does not show said is kept for when it does.
	if msg.host != a.host {
		if msg.err == nil {
			msg.host.described = &msg.described
		}

		return nil
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
	a.question, a.questionIn, a.questionLabel, a.questionText, a.questionFrom = msg.question, msg.conv, msg.label, msg.text, msg.who
	a.reply.clear()
	a.palette, a.form, a.confirming = nil, nil, nil
	msg.conv.add(&entry{kind: entrySaid, label: msg.label, text: msg.text})

	return a.notify("rumbo: " + msg.who + " asks: " + msg.text)
}

// askForTool is what answers a tool's question during a run: it opens the
// question, and waits for the answer, for the person to decline, or for
// the run to end.
func (a *app) askForTool(held *host, ctx context.Context, asked protocol.FrontendAskParams) (string, error) {
	opened := &question{secret: asked.Secret, answer: make(chan string, 1), declined: make(chan struct{})}
	a.send(toolAskedMsg{held, asked, opened})

	select {
	case answer := <-opened.answer:
		return answer, nil

	case <-opened.declined:
		return "", errors.New("the person declined to answer")

	case <-ctx.Done():
		a.send(askGoneMsg{opened})

		return "", ctx.Err()
	}
}

// decline closes a tool's question without an answer.
func (a *app) decline() {
	close(a.question.declined)

	a.question = nil
	a.reply.clear()
	a.questionIn.add(&entry{kind: entryTyped, text: "(declined)"})
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
