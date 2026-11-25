package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/LiasCode/rumbo/internal/frontends/render"
	"github.com/LiasCode/rumbo/protocol"
)

// A rumbo is one session, so the terminal UI holds one for each session it
// has open: the one that started it, and those it starts itself as the
// person opens other sessions of the directory, or new ones. It shows one
// at a time, follows the runs of all of them, and closes those nobody is
// using, since each costs its own processes.

// keptIdle is how many rumbos are kept open with nothing to do: neither
// shown nor working. Past that, the one shown longest ago is closed; its
// session is as it was, and opening it again starts a rumbo for it.
const keptIdle = 2

// How long a rumbo gets to start and say what it is, to list the sessions,
// and to end once its connection is closed, before it is killed.
const (
	startTimeout = 20 * time.Second
	listTimeout  = 10 * time.Second
	stopTimeout  = 5 * time.Second
)

// host is one rumbo the terminal UI is the front end of, and what it shows
// of its session.
type host struct {
	conn *protocol.FrontendConn
	info protocol.FrontendInitializeParams
	// stop ends a rumbo the terminal UI started, and waits for it; the one
	// that started the terminal UI has none, and ends when it does.
	stop func()
	// session is the host's session, as it last said, empty for a new one
	// until its first run makes it, and called what it is called; shown is
	// its conversation, and runs the run in progress.
	session string
	called  string
	shown   *conversation
	runs    []*flight
	// described is what the provider said it offers, once it was asked.
	described *protocol.DescribeResult
	// telling counts what was told to the run and has not reached the
	// model yet.
	telling int
	// afterLoad starts what waits for the session's history to show.
	afterLoad func() tea.Cmd
	// unseen is true once a run ended here while another session showed,
	// and left is when this one last stopped showing.
	unseen bool
	left   time.Time
}

// started is a rumbo just started, before the terminal UI holds it.
type started struct {
	conn *protocol.FrontendConn
	info protocol.FrontendInitializeParams
	stop func()
}

// rumbos is how the terminal UI reaches the other sessions: start begins a
// rumbo in dir for a session, or for a new one when it names none, list
// says the sessions of dir, most recently used first, and remove deletes
// one of them, which no rumbo may hold.
type rumbos struct {
	start  func(dir, session string) (started, error)
	list   func(dir string) ([]listing, error)
	remove func(dir, session string) error
}

// The messages of the rumbos.
type (
	// listedMsg is the sessions of the directory, for the sidebar.
	listedMsg struct {
		values []listing
		err    error
	}
	// openedMsg is a rumbo started for a session, or why it did not start;
	// then goes on once it shows.
	openedMsg struct {
		started started
		err     error
		then    func() tea.Cmd
	}
	// deleteMsg is the person agreeing to delete the session of a rumbo
	// held, and deletedMsg how deleting the session so called went.
	deleteMsg struct {
		host *host
	}
	deletedMsg struct {
		title string
		err   error
	}
	// rumbosMsg is what each rumbo held said of its processes.
	rumbosMsg struct {
		conv *conversation
		said []hostUsage
	}
)

// hostUsage is what a rumbo said its processes use, or why it did not.
type hostUsage struct {
	host      *host
	processes []protocol.ProcessUsage
	err       error
}

// hold makes a rumbo one of those the terminal UI holds.
func (a *app) hold(conn *protocol.FrontendConn, info protocol.FrontendInitializeParams, stop func()) *host {
	held := &host{conn: conn, info: info, stop: stop, session: info.Session, called: info.Title, left: a.now}

	held.shown = newConversation(info.Session, func(source string) string { return a.labelOf(held, source) })

	// A new session has no history to wait for.
	held.shown.loaded = info.Session == ""

	conn.AnswerRuns(func(ctx context.Context, asked protocol.FrontendAskParams) (string, error) {
		return a.askForTool(held, ctx, asked)
	})

	a.hosts = append(a.hosts, held)

	return held
}

// attach asks a rumbo just held what it has: what its provider offers, and
// what its session holds, and watches for its end.
func (a *app) attach(held *host) tea.Cmd {
	conn, send := held.conn, a.send

	go func() {
		<-conn.Done()

		send(hostGoneMsg{held})
	}()

	cmds := []tea.Cmd{a.describe(held, "")}

	if held.session != "" {
		held.shown.loading = true
		cmds = append(cmds, a.load(held))
	}

	return tea.Batch(cmds...)
}

// first is the rumbo that started the terminal UI, which lasts as long as
// it does.
func (a *app) first() *host {
	return a.hosts[0]
}

// holding returns the rumbo held for a session, or for a new one that
// nothing was asked in yet when the session is none.
func (a *app) holding(session string) *host {
	for _, held := range a.hosts {
		if held.session == session && (session != "" || len(held.runs) == 0) {
			return held
		}
	}

	return nil
}

// reaches reports whether the terminal UI reaches the other sessions of
// the directory, and so lists them.
func (a *app) reaches() bool {
	return a.rumbos.list != nil
}

// branches reports whether what the person asked in the session shown can
// be asked another way: its host makes the branch, and a rumbo can be
// started to go on in it.
func (a *app) branches() bool {
	return a.session != "" && a.offers("branch") && a.rumbos.start != nil
}

// list asks for the sessions of the directory, for the sidebar.
func (a *app) list() tea.Cmd {
	if a.rumbos.list == nil {
		return nil
	}

	list, dir := a.rumbos.list, a.first().info.Dir

	return func() tea.Msg {
		values, err := list(dir)

		return listedMsg{values, err}
	}
}

// listed takes the sessions of the directory in.
func (a *app) listed(msg listedMsg) {
	if msg.err != nil {
		return
	}

	a.sessions.list(msg.values)

	if a.focus != focusSidebar {
		a.sessions.choose(a.session)
	}
}

// open shows the session selected in the sidebar, or a new one.
func (a *app) open() tea.Cmd {
	a.focus = focusComposer

	return a.reach(a.sessions.chosen(), nil)
}

// reach shows a session, or a new one for none, and then goes on with
// then: in the rumbo held for it, or in one started for it.
func (a *app) reach(session string, then func() tea.Cmd) tea.Cmd {
	if held := a.holding(session); held != nil {
		cmd := a.show(held)

		if then != nil && a.host == held {
			cmd = tea.Batch(cmd, then())
		}

		return cmd
	}

	switch {
	case a.asking:
		a.notice = "a command is waiting for its answer"
		return nil
	case a.rumbos.start == nil:
		a.notice = "this terminal UI cannot start a rumbo for another session"
		return nil
	case a.opening:
		a.notice = "a session is being opened"
		return nil
	}

	a.opening = true

	start, dir := a.rumbos.start, a.first().info.Dir

	return func() tea.Msg {
		begun, err := start(dir, session)

		return openedMsg{begun, err, then}
	}
}

// opened shows the session a rumbo was started for.
func (a *app) opened(msg openedMsg) tea.Cmd {
	a.opening = false

	if msg.err != nil {
		a.notice = "the session could not be opened: " + msg.err.Error()
		return nil
	}

	held := a.hold(msg.started.conn, msg.started.info, msg.started.stop)
	cmds := []tea.Cmd{a.attach(held), a.show(held), a.list()}

	if msg.then != nil && a.host == held {
		cmds = append(cmds, msg.then())
	}

	return tea.Batch(cmds...)
}

// show makes a rumbo the one shown, unless a command waits for its answer
// in the one that is.
func (a *app) show(held *host) tea.Cmd {
	switch {
	case held == a.host:
		return nil
	case a.asking:
		a.notice = "a command is waiting for its answer"
		return nil
	}

	if a.focus == focusConversation {
		a.leaveConversation()
	}

	a.unselect()

	a.host.left = a.now
	a.host = held
	held.unseen = false
	a.menu, a.dismissed = nil, false
	a.sessions.choose(held.session)
	a.sessions.reveal(a.height - bodyTop)

	// The panel shows the session shown, and a rumbo left behind may be
	// one too many.
	return tea.Batch(a.readPanel(), a.tidy(keptIdle))
}

// idle reports whether a rumbo has nothing to do and can be closed: the
// terminal UI started it, it is not shown, nothing runs in it, no run
// ended there that the person has not seen, and nothing it asked waits for
// an answer.
func (a *app) idle(held *host) bool {
	asked := a.question != nil && a.questionIn == held.shown

	return held.stop != nil && held != a.host && len(held.runs) == 0 && !held.unseen && !asked
}

// tidy closes the rumbos with nothing to do past the kept that were shown
// last, so the sessions a person left do not go on costing their
// processes.
func (a *app) tidy(kept int) tea.Cmd {
	var idle []*host

	for _, held := range a.hosts {
		if a.idle(held) {
			idle = append(idle, held)
		}
	}

	if len(idle) <= kept {
		return nil
	}

	slices.SortStableFunc(idle, func(one, other *host) int { return one.left.Compare(other.left) })

	var cmds []tea.Cmd

	for _, held := range idle[:len(idle)-kept] {
		cmds = append(cmds, a.close(held))
	}

	return tea.Batch(cmds...)
}

// close lets go of a rumbo and ends it, which does not hold up the screen.
func (a *app) close(held *host) tea.Cmd {
	a.hosts = slices.DeleteFunc(a.hosts, func(other *host) bool { return other == held })
	stop := held.stop

	return func() tea.Msg {
		stop()

		return nil
	}
}

// delete asks the person to agree to deleting the session shown.
func (a *app) delete() tea.Cmd {
	return a.deleteHeld(a.host)
}

// deleteSession asks the person to agree to deleting a session of the
// directory, as the sidebar lists it: through the rumbo held for it, when
// there is one, and else from the command line, which refuses a session
// another rumbo has open.
func (a *app) deleteSession(session string) tea.Cmd {
	if held := a.holding(session); held != nil {
		return a.deleteHeld(held)
	}

	if a.rumbos.remove == nil {
		a.notice = "this terminal UI cannot delete a session"
		return nil
	}

	title := session

	for _, listed := range a.sessions.listed {
		if listed.session == session && listed.title != "" {
			title = render.FirstLine(listed.title)
		}
	}

	remove, dir := a.rumbos.remove, a.first().info.Dir
	asked := protocol.Function{
		Name:    "delete",
		Confirm: "This deletes “" + title + "” and its history, for good. The files it changed stay as they are.",
	}

	a.confirming = &confirmation{function: asked, run: func() tea.Msg { return deletedMsg{title, remove(dir, session)} }, conv: a.shown}

	return nil
}

// deleteHeld asks the person to agree to deleting the session of a rumbo
// held. The one rumbo started in is held by that rumbo for as long as the
// terminal UI lives, so it is not one to delete from here.
func (a *app) deleteHeld(held *host) tea.Cmd {
	why := ""

	switch {
	case a.rumbos.remove == nil:
		why = "this terminal UI cannot delete a session"
	case held.session == "":
		why = "this session is new, and holds nothing to delete"
	case held == a.first():
		why = "rumbo started in this session, and holds it until you quit; delete it then with:\nrumbo sessions rm " + held.session
	case len(held.runs) > 0:
		why = "this session is working; cancel its run first"
	}

	if why != "" {
		a.shown.add(&entry{kind: entryAnswer, text: why, failed: true})
		return nil
	}

	asked := protocol.Function{
		Name:    "delete",
		Confirm: "This deletes “" + titleOf(held) + "” and its history, for good. The files it changed stay as they are.",
	}

	a.confirming = &confirmation{function: asked, run: func() tea.Msg { return deleteMsg{held} }, conv: a.shown}

	return nil
}

// deleting lets go of the rumbo of a session the person agreed to delete,
// shows the first one in its place, and deletes the session once its rumbo
// ended, which held it until then.
func (a *app) deleting(held *host) tea.Cmd {
	a.asking = false

	if len(held.runs) > 0 {
		a.notice = "“" + titleOf(held) + "” started working, and was not deleted"
		return nil
	}

	a.hosts = slices.DeleteFunc(a.hosts, func(other *host) bool { return other == held })

	if a.question != nil && a.questionIn == held.shown {
		a.question = nil
		a.reply.clear()
	}

	var shows tea.Cmd

	if held == a.host {
		shows = a.show(a.first())
	}

	stop, remove, dir, session, title := held.stop, a.rumbos.remove, a.first().info.Dir, held.session, titleOf(held)

	return tea.Batch(shows, func() tea.Msg {
		stop()

		return deletedMsg{title, remove(dir, session)}
	})
}

// deleted says how deleting a session went, and lists the ones left.
func (a *app) deleted(msg deletedMsg) tea.Cmd {
	a.asking = false
	a.notice = "“" + msg.title + "” was deleted"

	if msg.err != nil {
		a.notice = "“" + msg.title + "” was not deleted: " + msg.err.Error()
	}

	return a.list()
}

// gone takes in that a rumbo ended. The terminal UI ends with the one that
// started it; another that ended by itself leaves its session as it was,
// and the first one shows in its place.
func (a *app) gone(held *host) tea.Cmd {
	if held == a.first() {
		return tea.Quit
	}

	if !slices.Contains(a.hosts, held) {
		return nil
	}

	// What is left of it is let go of.
	closing := a.close(held)

	if a.question != nil && a.questionIn == held.shown {
		a.question = nil
		a.reply.clear()
	}

	if held != a.host {
		return closing
	}

	// What waited for an answer from it never gets one.
	a.asking, a.invoking = false, 0
	a.host = a.first()
	a.sessions.choose(a.session)
	a.notice = "the rumbo of “" + titleOf(held) + "” ended, and its session is as it was"

	return tea.Batch(closing, a.readPanel(), a.list())
}

// shutdown ends every rumbo the terminal UI started, and waits for them, so
// none is left behind when it quits.
func (a *app) shutdown() {
	var ending sync.WaitGroup

	for _, held := range a.hosts {
		if held.stop != nil {
			ending.Go(held.stop)
		}
	}

	ending.Wait()
}

// elsewhere counts the runs in progress in the sessions not shown.
func (a *app) elsewhere() int {
	count := 0

	for _, held := range a.hosts {
		if held != a.host {
			count += len(held.runs)
		}
	}

	return count
}

// marked says what the sidebar marks its sessions with.
func (a *app) marked() marks {
	shown := marks{current: a.session, working: map[string]bool{}, unseen: map[string]bool{}, open: map[string]bool{}}

	for _, held := range a.hosts {
		if held.session == "" {
			continue
		}

		shown.open[held.session] = true
		shown.working[held.session] = len(held.runs) > 0
		shown.unseen[held.session] = held.unseen
	}

	return shown
}

// usage asks every rumbo held what its processes use, to show it in the
// conversation.
func (a *app) usage() tea.Cmd {
	hosts, conv := slices.Clone(a.hosts), a.shown
	a.asking = true

	return func() tea.Msg {
		said := make([]hostUsage, len(hosts))

		var asking sync.WaitGroup

		for index, held := range hosts {
			asking.Go(func() {
				result, err := held.conn.Processes()
				said[index] = hostUsage{held, result.Processes, err}
			})
		}

		asking.Wait()

		return rumbosMsg{conv, said}
	}
}

// rumbosText says what each rumbo held uses, one to a line, and what they
// use together. The rumbos the terminal UI started are processes of its
// own, which the rumbo that started it counts under its front end, so they
// and what is under them are taken out of that one's count, and each
// process is counted once.
func (a *app) rumbosText(said []hostUsage) string {
	others := map[int]bool{}

	for _, each := range said[1:] {
		if len(each.processes) > 0 {
			others[each.processes[0].PID] = true
		}
	}

	// under reports whether a process the first rumbo listed is another
	// rumbo, or was started by one, however far above it.
	parents := map[int]int{}

	for _, process := range said[0].processes {
		parents[process.PID] = process.Parent
	}

	under := func(pid int) bool {
		for range len(parents) + 1 {
			if others[pid] {
				return true
			}

			parent, listed := parents[pid]

			if !listed {
				return false
			}

			pid = parent
		}

		return false
	}

	var out strings.Builder

	table := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	fmt.Fprint(table, "SESSION\tIS\tPROCESSES\tMEMORY\tCPU\tTITLE\n")

	var count int
	var memory int64
	var cpu float64

	for index, each := range said {
		session, title := each.host.session, titleOf(each.host)

		if session == "" {
			session, title = "new", ""
		}

		standing := "open"

		switch {
		case len(each.host.runs) > 0:
			standing = "working"
		case each.host == a.host:
			standing = "shown"
		}

		if each.err != nil {
			fmt.Fprintf(table, "%s\t%s\t\t\t\t%s\n", session, standing, each.err.Error())
			continue
		}

		var processes int
		var held int64
		var used float64

		for _, process := range each.processes {
			if index == 0 && under(process.PID) {
				continue
			}

			processes++
			held += process.Memory
			used += process.CPU
		}

		fmt.Fprintf(table, "%s\t%s\t%d\t%s\t%s\t%s\n", session, standing, processes, render.Memory(held), render.ProcessorTime(used), cut(title, 40))

		count += processes
		memory += held
		cpu += used
	}

	_ = table.Flush()

	fmt.Fprintf(&out, "%d rumbos, %s, at most %s of memory, %s of processor time\n", len(said), render.Processes(count), render.Memory(memory), render.ProcessorTime(cpu))
	fmt.Fprintf(&out, "A rumbo left open with nothing to do is closed past the %d shown last; its session stays, and showing it starts one again. /processes says what the rumbo shown has running.", keptIdle)

	return out.String()
}

// startRumbo returns what starts a rumbo with program, the rumbo this
// terminal UI is of, as the host for a session of dir. The terminal UI
// answers it as it answered the first, with the configuration in force.
func startRumbo(program string, inForce json.RawMessage) func(dir, session string) (started, error) {
	return func(dir, session string) (started, error) {
		args := []string{"serve"}

		if session != "" {
			args = append(args, "-s", session)
		}

		hostReads, write, err := os.Pipe()

		if err != nil {
			return started{}, err
		}

		read, hostWrites, err := os.Pipe()

		if err != nil {
			hostReads.Close()
			write.Close()

			return started{}, err
		}

		// What it prints before it is attached says why it did not start;
		// none of it may reach the terminal, which is the screen's.
		var said tail

		cmd := exec.Command(program, args...)
		cmd.Dir = dir
		cmd.Stdin = hostReads
		cmd.Stdout = hostWrites
		cmd.Stderr = &said

		err = cmd.Start()

		hostReads.Close()
		hostWrites.Close()

		if err != nil {
			read.Close()
			write.Close()

			return started{}, err
		}

		ended := make(chan struct{})

		go func() {
			_ = cmd.Wait()

			close(ended)
		}()

		// Closing the connection ends a host. One that does not end is
		// killed.
		stop := func() {
			write.Close()

			select {
			case <-ended:
			case <-time.After(stopTimeout):
				_ = cmd.Process.Kill()
				<-ended
			}

			read.Close()
		}

		type dialed struct {
			conn *protocol.FrontendConn
			info protocol.FrontendInitializeParams
			err  error
		}

		dialing := make(chan dialed, 1)

		go func() {
			conn, info, err := protocol.DialFrontendConfigured(read, write, "tui", func(json.RawMessage) json.RawMessage { return inForce })
			dialing <- dialed{conn, info, err}
		}()

		select {
		case answer := <-dialing:
			if answer.err == nil {
				return started{answer.conn, answer.info, stop}, nil
			}

			stop()

			if why := said.String(); why != "" {
				return started{}, errors.New(why)
			}

			return started{}, answer.err

		case <-time.After(startTimeout):
			_ = cmd.Process.Kill()
			stop()

			return started{}, errors.New("rumbo did not start in time")
		}
	}
}

// listSessions returns what lists the sessions of a directory, as program,
// the rumbo this terminal UI is of, lists them for a program to read.
func listSessions(program string) func(dir string) ([]listing, error) {
	return func(dir string) ([]listing, error) {
		ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, program, "sessions", "-json")
		cmd.Dir = dir

		output, err := cmd.Output()

		if err != nil {
			return nil, err
		}

		var sessions []struct {
			Session string    `json:"session"`
			Title   string    `json:"title"`
			State   string    `json:"state"`
			Used    time.Time `json:"used"`
		}

		if err := json.Unmarshal(output, &sessions); err != nil {
			return nil, err
		}

		values := make([]listing, 0, len(sessions))

		for _, each := range sessions {
			values = append(values, listing{session: each.Session, title: each.Title, state: each.State, used: each.Used})
		}

		return values, nil
	}
}

// removeSession returns what deletes a session, as program, the rumbo this
// terminal UI is of, deletes one from the command line.
func removeSession(program string) func(dir, session string) error {
	return func(dir, session string) error {
		ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, program, "sessions", "rm", session)
		cmd.Dir = dir

		output, err := cmd.CombinedOutput()

		if why := strings.TrimSpace(string(output)); err != nil && why != "" {
			return errors.New(why)
		}

		return err
	}
}

// tail keeps the end of what is written to it, on one line.
type tail struct {
	mu   sync.Mutex
	kept bytes.Buffer
}

// How much of what a rumbo printed is kept.
const tailBytes = 2000

func (t *tail) Write(data []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.kept.Write(data)

	if extra := t.kept.Len() - tailBytes; extra > 0 {
		t.kept.Next(extra)
	}

	return len(data), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return strings.Join(strings.Fields(t.kept.String()), " ")
}
