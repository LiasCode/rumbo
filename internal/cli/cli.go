// Package cli is the command line and the place where the parts are wired
// together: it reads the settings, starts the front end and the provider, and
// hands the agent loop its dependencies.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/LiasCode/rumbo/internal/extension"
	"github.com/LiasCode/rumbo/internal/frontend"
	"github.com/LiasCode/rumbo/internal/provider"
	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/internal/tool"
	"github.com/LiasCode/rumbo/protocol"
)

const usage = `rumbo - a minimal agent harness

Usage:
  rumbo [flags] [objective]      work in the current directory, in a front end
  rumbo serve [flags]            be the host for a front end on stdin/stdout
  rumbo sessions ...             list, rename, and delete sessions
  rumbo skills                   list the skills the model is told of here, and those left out
  rumbo log <session>            print a session's entries as JSON lines
  rumbo request <session> [turn] print the request a turn sent the provider, as it was sent;
                                 without a turn, the last one
  rumbo config                   what is chosen and what applies, each value with where it comes from
  rumbo system ...               show and set the system settings
  rumbo provider ...             add, show, and set providers: their model, configuration, and options
  rumbo frontend ...             add, show, and set front ends
  rumbo extension ...            add, show, and set extensions, which give the model tools
  rumbo plugin ...               add, show, update, and remove plugins, which bring programs and skills
  rumbo provide <name>           run a provider of rumbo's own plugin on stdin/stdout
  rumbo attach <name>            run plain, or a front end of rumbo's own plugin, on descriptors 3/4
  rumbo extend mcp [command...]  run rumbo's own plugin's extension for an MCP server on stdin/stdout
  rumbo version                  print the version

Flags:
  -c             continue the latest session started in this directory
  -r             resume the unfinished run of that session, without a new objective
  -s <session>   continue the given session
  -p <provider>  provider of the session (default: the default_provider setting)
  -m <model>     model of the session (default: the model set for its provider)
  -o <n>=<v>     a provider option of the session, such as -o effort=high; may be
                 repeated, and an empty value gives one back to the provider's
  -f <name>      front end for this start (default: the frontend setting, else
                 tui in a terminal, else plain)

The session keeps what -p, -m, and -o choose, over the system settings.
`

// exitCode is returned through the error path to end the process with a
// front end's exit code, without printing anything more.
type exitCode int

func (code exitCode) Error() string {
	return fmt.Sprintf("exit status %d", int(code))
}

// Main runs the command line and returns the process exit code.
func Main(args []string) int {
	err := dispatch(args)

	if code, ok := errors.AsType[exitCode](err); ok {
		return int(code)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "rumbo:", err)
		return 1
	}

	return 0
}

func dispatch(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "help", "-h", "--help":
			fmt.Print(usage)
			return nil
		case "version", "-version", "--version":
			fmt.Println(describeVersion())
			return nil
		case "provide":
			return provide(args[1:])
		case "attach":
			return attach(args[1:])
		case "extend":
			return extend(args[1:])
		case "serve":
			return withStore(args[1:], serve)
		case "provider":
			return withStore(args[1:], programCommands(store.KindProvider))
		case "frontend":
			return withStore(args[1:], programCommands(store.KindFrontend))
		case "extension":
			return withStore(args[1:], programCommands(store.KindExtension))
		case "system":
			return withStore(args[1:], systemCommands)
		case "config":
			return withStore(args[1:], configCommand)
		case "sessions":
			return withStore(args[1:], sessionCommands)
		case "plugin":
			return withStore(args[1:], pluginCommands)
		case "skills":
			return withStore(args[1:], printSkills)
		case "log":
			return withStore(args[1:], printLog)
		case "request":
			return withStore(args[1:], printRequest)
		}
	}

	// A lone word close to a command is taken for a typo, not sent to the
	// model as a task; after -- it is a task whatever it looks like.
	if len(args) == 1 {
		if command, ok := mistyped(args[0]); ok {
			return fmt.Errorf("%q is not a command; did you mean %q? To give it to the model as a task, run: rumbo -- %s", args[0], command, args[0])
		}
	}

	return withStore(args, launch)
}

func withStore(args []string, command func(database store.Store, args []string) error) error {
	database, err := openStore()

	if err != nil {
		return err
	}

	defer database.Close()

	return command(database, args)
}

// launch is the main command: it starts the selected front end and serves
// it.
func launch(database store.Store, args []string) error {
	host, err := newHost(database, args)

	if err != nil {
		return err
	}

	defer host.close()

	program, err := findProgram(database, store.KindFrontend, host.settings.Frontend)

	if err != nil {
		return err
	}

	command, err := programCommand(program.Command)

	if err != nil {
		return err
	}

	process, err := frontend.Start(command)

	if err != nil {
		return err
	}

	host.info.Config = program.Config
	host.frontend = process.Pid()

	err = host.serve(process.Reader, process.Writer)

	// A front end that did not complete the handshake, or whose connection
	// broke, is stopped. Otherwise the host closes the connection and the
	// front end ends on its own, leaving the terminal as it found it, also
	// when a signal stopped the host.
	if _, signalled := errors.AsType[exitCode](err); err != nil && !signalled {
		process.Kill()
		process.Wait()

		return err
	}

	if code := process.Wait(); err == nil && code != 0 {
		return exitCode(code)
	}

	return err
}

// serve is the host for a front end that started rumbo itself, such as an
// editor: the protocol is on stdin and stdout.
func serve(database store.Store, args []string) error {
	host, err := newHost(database, args)

	if err != nil {
		return err
	}

	defer host.close()

	return host.serve(os.Stdin, os.Stdout)
}

// host is one rumbo process: one session, served to one front end. Another
// session is another rumbo.
type host struct {
	database store.Store
	settings config
	info     protocol.FrontendInitializeParams
	conn     *frontend.Conn
	// dir is the session's root: the directory rumbo was started in, or the
	// one the session it continues was.
	dir string
	// tools are the session's: what its model has read, and the processes
	// it left running, which stop with the host.
	tools *tool.Set
	// frontend is the system's id of the front end's process when the host
	// started it, and zero when the front end started the host.
	frontend int

	// setting keeps the functions answered in the background that change
	// what is kept one at a time.
	setting sync.Mutex

	// mu guards what follows: runs change it from their own goroutines, and
	// so do the functions answered in the background.
	mu sync.Mutex
	// choice is what the session uses.
	choice
	// session is nil until the first run of a new session makes it.
	session *store.Session
	// unlock releases the session while this host holds it.
	unlock func()
	// busy is what works in the session now, when something does.
	busy string
	// pools are the provider processes, by provider name.
	pools map[string]*provider.Pool
	// extensions are the extension processes, which start with the first
	// run after one is registered.
	extensions *extension.Set
}

// newHost resolves the session and the settings from the command line.
// Problems found here are reported before any front end starts.
func newHost(database store.Store, args []string) (*host, error) {
	flags := flag.NewFlagSet("rumbo", flag.ContinueOnError)
	flags.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	latest := flags.Bool("c", false, "")
	resume := flags.Bool("r", false, "")
	sessionID := flags.String("s", "", "")
	providerName := flags.String("p", "", "")
	model := flags.String("m", "", "")
	frontendName := flags.String("f", "", "")

	var options optionFlags
	flags.Var(&options, "o", "")

	if err := flags.Parse(args); err != nil {
		return nil, err
	}

	// The directory rumbo is started in is the root of the session, and
	// nothing above it is looked at.
	dir, err := os.Getwd()

	if err != nil {
		return nil, err
	}

	var session *store.Session

	switch {
	case *sessionID != "":
		found, err := database.Session(*sessionID)

		if err != nil {
			return nil, fmt.Errorf("session %s: %w", *sessionID, err)
		}

		session = &found

	case *latest || *resume:
		found, err := database.LatestSession(dir)

		if err != nil {
			return nil, fmt.Errorf("no session to continue in %s: %w", dir, err)
		}

		session = &found
	}

	// A session keeps working where it started, wherever it is continued
	// from.
	if session != nil {
		dir = session.Dir
	}

	settings, err := loadConfig(database, *frontendName)

	if err != nil {
		return nil, err
	}

	chosen, err := startChoice(database, settings, session, *providerName, *model, options)

	if err != nil {
		return nil, err
	}

	// A malformed stored value stops rumbo before a front end starts.
	if _, err := mergeOptions(chosen.options); err != nil {
		return nil, fmt.Errorf("the options of the session: %w", err)
	}

	objective := strings.TrimSpace(strings.Join(flags.Args(), " "))

	// A front end can choose a model before its first task; one given a task
	// to start with cannot.
	if chosen.model == "" && (objective != "" || *resume) {
		return nil, fmt.Errorf(`no model chosen for %[1]s: pass -m, or run "rumbo provider set %[1]s model=<model>"; "rumbo provider models %[1]s" lists them`, chosen.provider)
	}

	tools := tool.NewSet()

	created := &host{
		database: database,
		settings: settings,
		dir:      dir,
		tools:    tools,
		choice:   chosen,
		session:  session,
		pools:    map[string]*provider.Pool{},
	}

	// An extension starts once a front end is attached, which is where its
	// diagnostics go.
	created.extensions = extension.NewSet(func(name string) io.Writer { return created.conn.LogExtension(name) }, tools.Clip)

	// What the command line chose is the session's from now on, as use
	// makes it; a session still to be made stores it when it is made.
	if session != nil && (*providerName != "" || *model != "" || len(options) > 0) {
		if err := created.choose(chosen); err != nil {
			created.close()
			return nil, err
		}
	}

	created.info = protocol.FrontendInitializeParams{
		Dir:       dir,
		Provider:  chosen.provider,
		Model:     chosen.model,
		Objective: objective,
		Resume:    *resume,
		Functions: describedFunctions(),
	}

	if *resume && created.info.Objective != "" {
		return nil, errors.New("-r continues the unfinished run and takes no objective; use -c to give a new one")
	}

	if session != nil {
		created.info.Session, created.info.Title = session.ID, session.Title
	}

	return created, nil
}

// startChoice is what the session rumbo starts in uses: its own provider,
// model, and options, or, for a session still to be made, the default
// provider with the model set for it; what the command line chose goes over
// them. The options it gives are checked against what the provider
// describes, which starts it.
func startChoice(database store.Store, defaults config, session *store.Session, providerFlag, modelFlag string, changes optionFlags) (choice, error) {
	chosen := choice{provider: defaults.DefaultProvider}

	// A session that did not keep its provider takes the system's.
	if session != nil && session.Provider != "" {
		chosen = choice{provider: session.Provider, model: session.Model, options: session.Options}
	}

	// Options are for the provider they were chosen for, so another one
	// starts without them.
	if providerFlag != "" && providerFlag != chosen.provider {
		chosen = choice{provider: providerFlag}
	}

	if chosen.provider == "" {
		return chosen, fmt.Errorf(`no provider chosen: pass -p, or run "rumbo system set %s=<provider>"`, keyDefaultProvider)
	}

	program, err := findProgram(database, store.KindProvider, chosen.provider)

	if err != nil {
		if session != nil && session.Provider == chosen.provider && providerFlag == "" {
			return chosen, fmt.Errorf(`session %s uses the provider %q, which is not registered; register it again, or choose another with -p`, session.ID, chosen.provider)
		}

		return chosen, err
	}

	if chosen.model == "" {
		chosen.model = program.Model
	}

	if modelFlag != "" {
		chosen.model = modelFlag
	}

	if len(changes) == 0 {
		return chosen, nil
	}

	known, err := learnSettings(program, os.Stderr)

	if err != nil {
		return chosen, fmt.Errorf("%s did not start, and -o needs it to check the options: %w", chosen.provider, err)
	}

	chosen.options, _, err = chooseOptions(chosen.provider, known.offers.Options, chosen.options, changes)

	return chosen, err
}

// serve connects the front end on r and w and runs its objectives until it
// disconnects. SIGTERM and SIGHUP end it the same way, and the host then
// exits with the code a shell gives a process the signal killed.
func (h *host) serve(r io.Reader, w io.Writer) error {
	// The front end decides what Ctrl-C means and cancels through the
	// protocol, so the terminal's interrupt must not end the host under it.
	signal.Ignore(os.Interrupt)

	conn, err := frontend.Connect(r, w, h.info)

	if err != nil {
		return err
	}

	h.conn = conn

	// An extension that asks is told what the front end is told, but for
	// what the extensions themselves print, which one that printed what it
	// heard would be told without end.
	conn.Tap(func(event protocol.Event) {
		if event.Type != protocol.EventLog || event.Source != protocol.TargetExtension {
			h.extensions.Tell(event)
		}
	})

	// Before the front end is attached there is nothing to stop but the
	// process, which the signals still do by themselves.
	stops := make(chan os.Signal, 1)
	signal.Notify(stops, syscall.SIGTERM, syscall.SIGHUP)

	defer signal.Stop(stops)

	served := make(chan error, 1)

	go func() {
		served <- conn.Serve(frontend.Handlers{
			Run:       h.accept,
			Describe:  h.describe,
			Invoke:    h.invoke,
			Host:      h.invokeHost,
			Slow:      slowFunction,
			History:   h.history,
			Recall:    h.recall,
			Processes: h.processes,
		})
	}()

	select {
	case err := <-served:
		return err

	case received := <-stops:
		// The runs are cancelled and recorded, and Serve returns once they
		// end; the tools and the providers stop when the host closes.
		conn.Shutdown()
		<-served

		return exitCode(128 + int(received.(syscall.Signal)))
	}
}

// describe says what the session's provider offers and what a run uses by
// default. It starts the provider, since only the provider can say.
func (h *host) describe(ctx context.Context) (protocol.DescribeResult, error) {
	h.mu.Lock()
	chosen := h.choice
	h.mu.Unlock()

	pool, err := h.ready(ctx, chosen.provider)

	if err != nil {
		return protocol.DescribeResult{}, err
	}

	options, err := h.inForce(chosen, pool.Description())

	if err != nil {
		return protocol.DescribeResult{}, err
	}

	return protocol.DescribeResult{
		Provider:   chosen.provider,
		Model:      chosen.model,
		Options:    options,
		Offers:     pool.Description(),
		Extensions: h.extensionFunctions(ctx),
	}, nil
}

// extensionFunctions says the functions the extensions offer, which starts
// them, as a run does. When they do not all start the person is told why,
// and what the provider offers is said all the same.
func (h *host) extensionFunctions(ctx context.Context) []protocol.ExtensionOffer {
	programs, err := extensionPrograms(h.database)

	if err == nil {
		_, err = h.extensions.Start(ctx, programs, reserved())
	}

	if err != nil {
		fmt.Fprintf(h.conn.LogExtension("extensions"), "their functions are not offered: %v\n", err)

		return nil
	}

	return h.extensions.Functions()
}

// invoke calls a function of the session's provider, or of the
// extension the call names, passed on as it is. What the function says and asks while it works goes through
// dialog, unread.
func (h *host) invoke(ctx context.Context, params protocol.InvokeParams, dialog protocol.Dialog) (json.RawMessage, error) {
	if params.Target == protocol.TargetExtension {
		return h.extensions.Invoke(ctx, params.Extension, params.Name, params.Params, dialog)
	}

	h.mu.Lock()
	name := h.provider
	h.mu.Unlock()

	pool, err := h.ready(ctx, name)

	if err != nil {
		return nil, err
	}

	return pool.Invoke(ctx, params.Name, params.Params, dialog)
}

func (h *host) close() {
	h.tools.Close()
	h.extensions.Close()

	h.mu.Lock()
	defer h.mu.Unlock()

	for _, pool := range h.pools {
		_ = pool.Close()
	}

	if h.unlock != nil {
		h.unlock()
		h.unlock = nil
	}
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()

	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
