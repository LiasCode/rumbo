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

	"rumbo/internal/frontend"
	"rumbo/internal/provider"
	"rumbo/internal/store"
	"rumbo/internal/tool"
	"rumbo/protocol"
)

const usage = `rumbo - a minimal agent harness

Usage:
  rumbo [flags] [objective]      work in the current directory, in a front end
  rumbo serve [flags]            be the host for a front end on stdin/stdout
  rumbo sessions                 list the sessions of the current project
  rumbo log <session>            print a session's entries as JSON lines
  rumbo config ...               read and write settings
  rumbo provider ...             manage providers
  rumbo frontend ...             manage front ends
  rumbo provide <name>           run a built-in provider on stdin/stdout
  rumbo attach <name>            run a built-in front end on descriptors 3/4

Flags:
  -c            continue the latest session of the current project
  -r            resume the unfinished run of that session, without a new objective
  -s <session>  continue the given session
  -m <model>    provider and model for this run, as provider/model
  -o <n>=<v>    a provider option for this run, such as -o effort=high; may be repeated
  -f <name>     front end for this run (default: tui in a terminal, else plain)
  -single       work in one session at a time; by default the host works in
                several sessions of the project at once
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
		case "provide":
			return provide(args[1:])
		case "attach":
			return attach(args[1:])
		case "serve":
			return withStore(args[1:], serve)
		case "provider":
			return withStore(args[1:], programCommands(store.KindProvider))
		case "frontend":
			return withStore(args[1:], programCommands(store.KindFrontend))
		case "config":
			return withStore(args[1:], configCommands)
		case "sessions":
			return withStore(args[1:], listSessions)
		case "log":
			return withStore(args[1:], printLog)
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

	program, err := database.Program(store.KindFrontend, host.settings.Frontend)

	if err != nil {
		return fmt.Errorf(`front end %q is not registered; see "rumbo frontend list"`, host.settings.Frontend)
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

// host is one rumbo process serving one front end, in one project. It can
// work in several sessions of the project at once.
type host struct {
	database store.Store
	settings config
	// root is the project the sessions belong to.
	root string
	// single makes the host work in one session at a time, as -single asks.
	single bool
	info   protocol.FrontendInitializeParams
	conn   *frontend.Conn
	// tools holds the processes the sessions leave running, which stop with
	// the host. Each session has tools of its own besides.
	tools *tool.Set

	// mu guards what follows, and the places in it but for their tools:
	// runs change them from their own goroutines, and so do the functions
	// answered in the background.
	mu sync.Mutex
	// current is where a run that names no session goes.
	current *place
	// places are the sessions this host has worked in, by id.
	places map[string]*place
	// busy are the places something works in now, and what it is.
	busy map[*place]string
	// pools are the provider processes, by provider name.
	pools map[string]*provider.Pool
}

// newHost resolves the session and the settings from the command line.
// Problems found here are reported before any front end starts.
func newHost(database store.Store, args []string) (*host, error) {
	flags := flag.NewFlagSet("rumbo", flag.ContinueOnError)
	flags.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	latest := flags.Bool("c", false, "")
	resume := flags.Bool("r", false, "")
	sessionID := flags.String("s", "", "")
	model := flags.String("m", "", "")
	frontendName := flags.String("f", "", "")
	single := flags.Bool("single", false, "")

	var options optionFlags
	flags.Var(&options, "o", "")

	if err := flags.Parse(args); err != nil {
		return nil, err
	}

	dir, root, err := workingProject()

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
		found, err := database.LatestSession(root)

		if err != nil {
			return nil, fmt.Errorf("no session to continue in %s: %w", root, err)
		}

		session = &found
	}

	// A session keeps working where it started, wherever it is continued
	// from, and with the model it uses unless -m chooses another for now.
	chosen := *model

	if session != nil {
		dir, root = session.Dir, session.Project

		if chosen == "" && session.Provider != "" {
			chosen = session.Provider + "/" + session.Model
		}
	}

	settings, err := loadConfig(database, root, chosen, *frontendName, options.object())

	if err != nil {
		return nil, err
	}

	if _, err := database.Program(store.KindProvider, settings.Provider); err != nil {
		if session != nil && session.Provider == settings.Provider && *model == "" {
			return nil, fmt.Errorf(`session %s uses the provider %q, which is not registered; register it again, or choose another with -m`, session.ID, settings.Provider)
		}

		return nil, fmt.Errorf(`provider %q is not registered; see "rumbo provider list"`, settings.Provider)
	}

	tools := tool.NewSet()

	created := &host{
		database: database,
		settings: settings,
		root:     root,
		single:   *single,
		tools:    tools,
		current: &place{
			choice:  choice{provider: settings.Provider, model: settings.Model, options: settings.Options},
			session: session,
			dir:     dir,
			tools:   tools.Separate(),
		},
		places: map[string]*place{},
		busy:   map[*place]string{},
		pools:  map[string]*provider.Pool{},
	}

	created.info = protocol.FrontendInitializeParams{
		Dir:       dir,
		Provider:  settings.Provider,
		Model:     settings.Model,
		Objective: strings.TrimSpace(strings.Join(flags.Args(), " ")),
		Resume:    *resume,
		Functions: describedFunctions(),
	}

	if *resume && created.info.Objective != "" {
		return nil, errors.New("-r continues the unfinished run and takes no objective; use -c to give a new one")
	}

	if session != nil {
		created.places[session.ID] = created.current
		created.info.Session = session.ID
	}

	return created, nil
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

	// Before the front end is attached there is nothing to stop but the
	// process, which the signals still do by themselves.
	stops := make(chan os.Signal, 1)
	signal.Notify(stops, syscall.SIGTERM, syscall.SIGHUP)

	defer signal.Stop(stops)

	served := make(chan error, 1)

	go func() {
		served <- conn.Serve(frontend.Handlers{
			Run:      h.accept,
			Describe: h.describe,
			Invoke:   h.invoke,
			Host:     h.invokeHost,
			Slow:     slowFunction,
			History:  h.history,
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

// describe says what the current session's provider offers and what a run
// there uses by default. It starts the provider, since only the provider
// can say.
func (h *host) describe(ctx context.Context) (protocol.DescribeResult, error) {
	h.mu.Lock()
	chosen := h.current.choice
	h.mu.Unlock()

	pool, err := h.pool(chosen.provider)

	if err != nil {
		return protocol.DescribeResult{}, err
	}

	if err := pool.Ready(ctx); err != nil {
		return protocol.DescribeResult{}, err
	}

	return protocol.DescribeResult{
		Provider: chosen.provider,
		Model:    chosen.model,
		Options:  chosen.options,
		Offers:   pool.Description(),
	}, nil
}

// invoke calls a function of the current session's provider, passed on as
// it is. What the function says and asks while it works goes through
// dialog, unread.
func (h *host) invoke(ctx context.Context, params protocol.InvokeParams, dialog protocol.Dialog) (json.RawMessage, error) {
	h.mu.Lock()
	name := h.current.provider
	h.mu.Unlock()

	pool, err := h.pool(name)

	if err != nil {
		return nil, err
	}

	return pool.Invoke(ctx, params.Name, params.Params, dialog)
}

func (h *host) close() {
	h.tools.Close()

	h.mu.Lock()
	defer h.mu.Unlock()

	for _, pool := range h.pools {
		_ = pool.Close()
	}

	for _, known := range h.places {
		known.release()
	}

	h.current.release()
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()

	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
