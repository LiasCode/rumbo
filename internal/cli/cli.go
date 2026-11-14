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

	"rumbo/internal/agent"
	"rumbo/internal/ambient"
	"rumbo/internal/frontend"
	"rumbo/internal/frontends/render"
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

	if err := host.serve(process.Reader, process.Writer); err != nil {
		process.Kill()
		process.Wait()

		return err
	}

	if code := process.Wait(); code != 0 {
		return exitCode(code)
	}

	return nil
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

// host is one rumbo process serving one front end.
type host struct {
	database store.Store
	settings config
	// root is the project the session belongs to.
	root    string
	info    protocol.FrontendInitializeParams
	session *store.Session

	conn    *frontend.Conn
	process provider.Provider
	worker  *agent.Agent
	// unlock releases the session, once one is in use.
	unlock func()
	tools  *tool.Set
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

	var options optionFlags
	flags.Var(&options, "o", "")

	if err := flags.Parse(args); err != nil {
		return nil, err
	}

	dir, root, err := workingProject()

	if err != nil {
		return nil, err
	}

	created := &host{database: database, root: root, tools: tool.NewSet()}

	switch {
	case *sessionID != "":
		found, err := database.Session(*sessionID)

		if err != nil {
			return nil, fmt.Errorf("session %s: %w", *sessionID, err)
		}

		created.session = &found

	case *latest || *resume:
		found, err := database.LatestSession(root)

		if err != nil {
			return nil, fmt.Errorf("no session to continue in %s: %w", root, err)
		}

		created.session = &found
	}

	// A session keeps working where it started, wherever it is continued from.
	if created.session != nil {
		dir, created.root = created.session.Dir, created.session.Project
	}

	if created.settings, err = loadConfig(database, created.root, *model, *frontendName, options.object()); err != nil {
		return nil, err
	}

	if _, err := database.Program(store.KindProvider, created.settings.Provider); err != nil {
		return nil, fmt.Errorf(`provider %q is not registered; see "rumbo provider list"`, created.settings.Provider)
	}

	created.info = protocol.FrontendInitializeParams{
		Dir:       dir,
		Provider:  created.settings.Provider,
		Model:     created.settings.Model,
		Objective: strings.TrimSpace(strings.Join(flags.Args(), " ")),
		Resume:    *resume,
		Functions: describedFunctions(),
	}

	if *resume && created.info.Objective != "" {
		return nil, errors.New("-r continues the unfinished run and takes no objective; use -c to give a new one")
	}

	if created.session != nil {
		created.info.Session = created.session.ID
	}

	return created, nil
}

// serve connects the front end on r and w and runs its objectives until it
// disconnects.
func (h *host) serve(r io.Reader, w io.Writer) error {
	// The front end decides what Ctrl-C means and cancels through the
	// protocol, so the terminal's interrupt must not end the host under it.
	signal.Ignore(os.Interrupt)

	conn, err := frontend.Connect(r, w, h.info)

	if err != nil {
		return err
	}

	h.conn = conn

	return conn.Serve(frontend.Handlers{Run: h.run, Describe: h.describe, Invoke: h.invoke})
}

// describe says what the provider offers and what a run uses by default.
// It starts the provider, since only the provider can say.
func (h *host) describe(context.Context) (protocol.DescribeResult, error) {
	if err := h.start(); err != nil {
		return protocol.DescribeResult{}, err
	}

	return protocol.DescribeResult{
		Provider: h.settings.Provider,
		Model:    h.settings.Model,
		Options:  h.settings.Options,
		Offers:   h.process.Description(),
	}, nil
}

// invoke answers a function call: the host's own, or the provider's, which
// is passed on as it is.
func (h *host) invoke(ctx context.Context, params protocol.InvokeParams) (json.RawMessage, error) {
	if params.Target == protocol.TargetHost {
		result, err := h.invokeHost(ctx, params.Name, params.Params)

		if err != nil {
			return nil, err
		}

		return json.Marshal(result)
	}

	if err := h.start(); err != nil {
		return nil, err
	}

	return h.process.Invoke(ctx, params.Name, params.Params)
}

// run works on one run request. The provider and the session are created on
// first use, so their errors reach the user through the front end and an
// unused start leaves nothing behind.
func (h *host) run(ctx context.Context, params protocol.RunParams) (string, error) {
	if params.Resume && h.session == nil {
		return "", errors.New("there is no session to resume")
	}

	if err := h.start(); err != nil {
		return h.info.Session, err
	}

	// What the run does not choose keeps the value in force by default.
	options, err := mergeOptions(h.settings.Options, params.Options)

	if err != nil {
		return h.info.Session, err
	}

	h.worker.Options = options
	h.worker.Model = h.settings.Model

	if params.Model != "" {
		h.worker.Model = params.Model
	}

	if h.session == nil {
		created, err := h.database.CreateSession(h.info.Dir, h.root, render.FirstLine(params.Objective))

		if err != nil {
			return "", err
		}

		h.session = &created
		h.info.Session = created.ID
	}

	if err := h.lock(); err != nil {
		return h.session.ID, err
	}

	if params.Resume {
		return h.session.ID, h.worker.Resume(ctx, *h.session)
	}

	return h.session.ID, h.worker.Run(ctx, *h.session, params.Objective)
}

// start runs the provider and builds the agent around it, once.
func (h *host) start() error {
	if h.worker != nil {
		return nil
	}

	program, err := h.database.Program(store.KindProvider, h.settings.Provider)

	if err != nil {
		return err
	}

	command, err := programCommand(program.Command)

	if err != nil {
		return err
	}

	process, err := provider.Start(context.Background(), command, program.Config, h.conn.Log())

	if err != nil {
		return err
	}

	h.process = process

	h.worker = &agent.Agent{
		Store:        h.database,
		Provider:     process,
		Observer:     h.conn,
		Tools:        h.tools.Tools(),
		Collectors:   ambient.Defaults(),
		ProviderName: h.settings.Provider,
		Model:        h.settings.Model,
		MaxTokens:    h.settings.MaxTokens,
		MaxTurns:     h.settings.MaxTurns,
		Compaction:   h.settings.Compaction,
		RetryWaits:   agent.DefaultRetryWaits,
	}

	return nil
}

func (h *host) close() {
	h.tools.Close()

	if h.process != nil {
		_ = h.process.Close()
	}

	h.release()
}

// lock takes the session for this process, if it does not have it yet.
func (h *host) lock() error {
	if h.unlock != nil {
		return nil
	}

	unlock, err := h.database.LockSession(h.session.ID)

	if err != nil {
		return fmt.Errorf("session %s: %w", h.session.ID, err)
	}

	h.unlock = unlock

	return nil
}

// release gives up the session this process holds, if any.
func (h *host) release() {
	if h.unlock != nil {
		h.unlock()
		h.unlock = nil
	}
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()

	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
