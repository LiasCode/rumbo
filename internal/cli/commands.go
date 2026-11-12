package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"rumbo/internal/frontends/plain"
	"rumbo/internal/frontends/tui"
	"rumbo/internal/providers/anthropic"
	"rumbo/internal/providers/openai"
	"rumbo/internal/store"
	"rumbo/protocol"
)

// provide runs a built-in provider as a protocol server on stdin and stdout.
func provide(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: rumbo provide <anthropic|openai>")
	}

	var handler protocol.Handler

	switch args[0] {
	case "anthropic":
		handler = anthropic.New()
	case "openai":
		handler = openai.New()
	default:
		return fmt.Errorf("unknown built-in provider %q", args[0])
	}

	return protocol.Serve(context.Background(), os.Stdin, os.Stdout, handler)
}

// attach runs a built-in front end on the protocol's file descriptors.
func attach(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: rumbo attach <tui|plain>")
	}

	// Started by hand these descriptors are not open, and the front end
	// reports that the host did not answer.
	reader := os.NewFile(protocol.FrontendReadFD, "host-to-frontend")
	writer := os.NewFile(protocol.FrontendWriteFD, "frontend-to-host")

	var code int

	switch args[0] {
	case "tui":
		code = tui.Run(reader, writer)
	case "plain":
		code = plain.Run(reader, writer)
	default:
		return fmt.Errorf("unknown built-in front end %q", args[0])
	}

	if code != 0 {
		return exitCode(code)
	}

	return nil
}

// programHelp explains what each kind of program must speak.
var programHelp = map[string]string{
	store.KindProvider: "A provider is any program that speaks the Agent Provider Protocol on its\nstdin and stdout.",
	store.KindFrontend: "A front end is any program that speaks the Agent Frontend Protocol on file\ndescriptors 3 (read) and 4 (write). It keeps the terminal on stdin and stdout.",
}

func programUsage(kind string) string {
	return fmt.Sprintf(
		"usage:\n  rumbo %[1]s list\n  rumbo %[1]s add [-config <json>] <name> <command> [args...]\n  rumbo %[1]s set <name> <field>=<value>...   change its configuration; an empty value removes the field\n  rumbo %[1]s show <name>                     what is registered; a provider is started and says what it offers\n  rumbo %[1]s rm <name>\n\n%[2]s\n\"{self}\" as the command stands for the rumbo executable.",
		kind, programHelp[kind],
	)
}

// programCommands returns the list, add, and rm commands for one kind of
// registered program.
func programCommands(kind string) func(database store.Store, args []string) error {
	return func(database store.Store, args []string) error {
		return manageProgram(database, kind, args)
	}
}

func manageProgram(database store.Store, kind string, args []string) error {
	help := programUsage(kind)

	if len(args) == 0 {
		return errors.New(help)
	}

	switch args[0] {
	case "list":
		programs, err := database.Programs(kind)

		if err != nil {
			return err
		}

		for _, program := range programs {
			fmt.Printf("%s\t%s\t%s\n", program.Name, strings.Join(program.Command, " "), program.Config)
		}

		return nil

	case "add":
		flags := flag.NewFlagSet(kind+" add", flag.ContinueOnError)
		config := flags.String("config", "{}", "")

		if err := flags.Parse(args[1:]); err != nil {
			return err
		}

		rest := flags.Args()

		if len(rest) < 2 {
			return errors.New(help)
		}

		return registerProgram(database, kind, rest[0], rest[1:], json.RawMessage(*config))

	case "set":
		if len(args) < 3 {
			return errors.New(help)
		}

		program, err := configureProgram(database, kind, args[1], args[2:])

		if err != nil {
			return err
		}

		fmt.Println(program.Config)

		return nil

	case "show":
		if len(args) != 2 {
			return errors.New(help)
		}

		if kind == store.KindProvider {
			described, err := inspectProvider(database, args[1], os.Stderr)

			if err != nil {
				return err
			}

			fmt.Println(described)

			return nil
		}

		program, err := database.Program(kind, args[1])

		if err != nil {
			return fmt.Errorf("%s %q is not registered", kind, args[1])
		}

		fmt.Println(describeProgram(program))

		return nil

	case "rm":
		if len(args) != 2 {
			return errors.New(help)
		}

		return database.DeleteProgram(kind, args[1])
	}

	return errors.New(help)
}

func configUsage() string {
	return "usage:\n" +
		"  rumbo config list                     every setting as it applies here, and where it comes from\n" +
		"  rumbo config get [-p] <key>           the value stored globally, or for this project with -p\n" +
		"  rumbo config set [-p] <key> <value>\n" +
		"  rumbo config unset [-p] <key>\n\n" +
		"-p applies to the project the current directory is part of, instead of globally.\n\nsettings:\n" + settingsHelp()
}

func configCommands(database store.Store, args []string) error {
	if len(args) == 0 {
		return errors.New(configUsage())
	}

	_, root, err := workingProject()

	if err != nil {
		return err
	}

	if args[0] == "list" {
		settings, err := effectiveSettings(database, root)

		if err != nil {
			return err
		}

		fmt.Println(describeSettings(settings))

		return nil
	}

	flags := flag.NewFlagSet("config", flag.ContinueOnError)
	project := flags.Bool("p", false, "")

	if err := flags.Parse(args[1:]); err != nil {
		return err
	}

	rest := flags.Args()
	scope := store.GlobalScope

	if *project {
		scope = root
	}

	switch {
	case args[0] == "get" && len(rest) == 1:
		value, found, err := database.Setting(scope, rest[0])

		if err != nil {
			return err
		}

		if !found {
			return fmt.Errorf("%s is not set there", rest[0])
		}

		fmt.Println(value)

		return nil

	case args[0] == "set" && len(rest) == 2:
		return setSetting(database, scope, rest[0], rest[1])

	case args[0] == "unset" && len(rest) == 1:
		return unsetSetting(database, scope, rest[0])
	}

	return errors.New(configUsage())
}

func listSessions(database store.Store, _ []string) error {
	_, root, err := workingProject()

	if err != nil {
		return err
	}

	sessions, err := database.Sessions(root)

	if err != nil {
		return err
	}

	for _, session := range sessions {
		fmt.Printf("%s\t%s\t%s\n", session.ID, session.ActiveAt.Format("2006-01-02 15:04"), session.Title)
	}

	return nil
}

// printLog writes the session's entries, one JSON object per line. The
// entries are the complete record: replaying them rebuilds every request.
func printLog(database store.Store, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: rumbo log <session>")
	}

	if _, err := database.Session(args[0]); err != nil {
		return fmt.Errorf("session %s: %w", args[0], err)
	}

	entries, err := database.Entries(args[0])

	if err != nil {
		return err
	}

	encoder := json.NewEncoder(os.Stdout)

	for _, entry := range entries {
		line := struct {
			Seq     int64           `json:"seq"`
			Time    string          `json:"time"`
			Kind    string          `json:"kind"`
			Payload json.RawMessage `json:"payload"`
		}{entry.Seq, entry.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"), entry.Kind, entry.Payload}

		if err := encoder.Encode(line); err != nil {
			return err
		}
	}

	return nil
}
