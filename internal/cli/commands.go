package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/LiasCode/rumbo/internal/frontends/plain"
	"github.com/LiasCode/rumbo/internal/frontends/tui"
	"github.com/LiasCode/rumbo/internal/providers/anthropic"
	"github.com/LiasCode/rumbo/internal/providers/chatgpt"
	"github.com/LiasCode/rumbo/internal/providers/openai"
	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/protocol"
)

// provide runs a built-in provider as a protocol server on stdin and stdout.
func provide(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: rumbo provide <anthropic|openai|chatgpt>")
	}

	var handler protocol.Handler

	switch args[0] {
	case "anthropic":
		handler = anthropic.New()
	case "openai":
		handler = openai.New()
	case "chatgpt":
		handler = chatgpt.New()
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
	options := ""

	if kind == store.KindProvider {
		indent := "\n" + strings.Repeat(" ", 49)
		options = "\n  rumbo provider options <name> [<option>=<value>...]" +
			indent + "the option values it gets with every request, in the sessions" +
			indent + "that choose none of their own; an empty value gives one back" +
			indent + "to the provider's default"
	}

	return fmt.Sprintf(
		"usage:\n  rumbo %[1]s list\n  rumbo %[1]s add [-config <json>] <name> <command> [args...]\n  rumbo %[1]s config <name> <field>=<value>...   change its start configuration; an empty value gives a field\n                                                 back to the %[1]s's default%[3]s\n  rumbo %[1]s show <name>                     what is registered; a provider is started and says what it offers\n  rumbo %[1]s rm <name>\n\n%[2]s\n\"{self}\" as the command stands for the rumbo executable.\nA %[1]s gives a default for every field of its configuration it was not given,\nwhich is stored with it the first time it starts%[4]s.",
		kind, programHelp[kind], options, programStart[kind],
	)
}

// programStart says when a kind of program first starts, which is when its
// defaults are stored.
var programStart = map[string]string{
	store.KindProvider: ", which for one added here is when it is added",
	store.KindFrontend: "",
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
			line := fmt.Sprintf("%s\t%s\t%s", program.Name, strings.Join(program.Command, " "), program.Config)

			if kind == store.KindProvider {
				line += "\t" + string(program.Options)
			}

			fmt.Println(line)
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

		if kind != store.KindProvider {
			return registerProgram(database, kind, rest[0], rest[1:], json.RawMessage(*config))
		}

		undecided, err := registerProvider(database, rest[0], rest[1:], json.RawMessage(*config), os.Stderr)

		if note := undecidedNote(rest[0], undecided); err == nil && note != "" {
			fmt.Fprintln(os.Stderr, "rumbo: registered"+note)
		}

		return err

	case "config":
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

	case "options":
		if kind != store.KindProvider {
			return errors.New(help)
		}

		return providerOptions(database, args[1:])
	}

	return errors.New(help)
}

// providerOptions changes the option values stored for a provider, when
// changes are given, and shows them.
func providerOptions(database store.Store, args []string) error {
	if len(args) == 0 {
		return errors.New(programUsage(store.KindProvider))
	}

	if len(args) > 1 {
		if err := changeOptions(database, args[0], args[1:]); err != nil {
			return err
		}
	}

	described, err := describeOptions(database, args[0])

	if err != nil {
		return err
	}

	fmt.Println(described)

	return nil
}

func systemUsage() string {
	return "usage:\n" +
		"  rumbo system list                every system setting, and where its value comes from\n" +
		"  rumbo system get <key>           the value stored\n" +
		"  rumbo system set <key> <value>\n" +
		"  rumbo system unset <key>\n\n" +
		"System settings are rumbo's own, the same everywhere. What a session chooses\n" +
		"for itself, with -p, -m, -o, use, or session-options, wins over them there.\n" +
		"A provider's options are set with \"rumbo provider options\".\n\nsystem settings:\n" + settingsHelp()
}

func systemCommands(database store.Store, args []string) error {
	switch {
	case len(args) == 1 && args[0] == "list":
		settings, err := effectiveSettings(database)

		if err != nil {
			return err
		}

		ignored, err := ignoredSettings(database)

		if err != nil {
			return err
		}

		fmt.Println(describeSettings(settings, ignored))

		return nil

	case len(args) == 2 && args[0] == "get":
		value, err := storedSetting(database, args[1])

		if err != nil {
			return err
		}

		fmt.Println(value)

		return nil

	case len(args) == 3 && args[0] == "set":
		return setSetting(database, args[1], args[2])

	case len(args) == 2 && args[0] == "unset":
		return unsetSetting(database, args[1])
	}

	return errors.New(systemUsage())
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
