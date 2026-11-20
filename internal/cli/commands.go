package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/LiasCode/rumbo/internal/agent"
	"github.com/LiasCode/rumbo/internal/frontends/plain"
	"github.com/LiasCode/rumbo/internal/frontends/tui"
	"github.com/LiasCode/rumbo/internal/providers/anthropic"
	"github.com/LiasCode/rumbo/internal/providers/chatgpt"
	"github.com/LiasCode/rumbo/internal/providers/openai"
	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/internal/transcript"
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
		options = " A provider's options are fields\nof its configuration too: effort=high gives every session that chooses no other\nthat effort."
	}

	return fmt.Sprintf(
		"usage:\n  rumbo %[1]s list\n  rumbo %[1]s add [-config <json>] <name> <command> [args...]\n  rumbo %[1]s config <name> <field>=<value>...   change its configuration; an empty value gives a field\n                                                 back to its default\n  rumbo %[1]s show <name>                     how it is started and what was chosen for it; a provider\n                                                 is started, and says its defaults and what it offers\n  rumbo %[1]s rm <name>                       remove one you added, or what you chose for a built-in\n\n%[2]s\n\"{self}\" as the command stands for the rumbo executable.\nOnly what you choose is kept, in %[4]s; what you do not is the\n%[1]s's own default.%[3]s",
		kind, programHelp[kind], options, configFile,
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
		programs, err := listPrograms(database, kind)

		if err != nil {
			return err
		}

		for _, program := range programs {
			fmt.Printf("%s\t%s\t%s\n", program.Name, strings.Join(program.Command, " "), shown(program.Config))
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

		return registerProvider(database, rest[0], rest[1:], json.RawMessage(*config), os.Stderr)

	case "config":
		if len(args) < 3 {
			return errors.New(help)
		}

		program, err := configureProgram(database, kind, args[1], args[2:])

		if err != nil {
			return err
		}

		fmt.Println(shown(program.Config))

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

		program, err := findProgram(database, kind, args[1])

		if err != nil {
			return err
		}

		fmt.Println(describeProgram(program))

		return nil

	case "rm":
		if len(args) != 2 {
			return errors.New(help)
		}

		removed, err := removeProgram(database, kind, args[1])

		if err != nil {
			return err
		}

		fmt.Println(removed)

		return nil

	case "options":
		if kind == store.KindProvider {
			return errors.New(`"rumbo provider options" is now "rumbo provider config": a provider's options are fields of its configuration`)
		}
	}

	return errors.New(help)
}

func systemUsage() string {
	kept := configFile

	if path, err := ConfigPath(); err == nil {
		kept = path
	}

	return "usage:\n" +
		"  rumbo system list                every system setting, and where its value comes from\n" +
		"  rumbo system get <key>           the value stored\n" +
		"  rumbo system set <key> <value>\n" +
		"  rumbo system unset <key>\n\n" +
		"System settings are rumbo's own, the same everywhere. What a session chooses\n" +
		"for itself, with -p, -m, -o, use, or session-options, wins over them there.\n" +
		"A provider's options are set with \"rumbo provider config\".\n" +
		"What you set is kept in " + kept + ", which you can edit.\n\nsystem settings:\n" + settingsHelp()
}

func systemCommands(database store.Store, args []string) error {
	switch {
	case len(args) == 1 && args[0] == "list":
		settings, err := effectiveSettings(database)

		if err != nil {
			return err
		}

		fmt.Println(describeSettings(settings))

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

// printRequest writes what the provider was sent for a turn of a session,
// as it was sent: the params of its complete, rebuilt from the entries
// before the response. Without a turn, the last one.
func printRequest(database store.Store, args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return errors.New("usage: rumbo request <session> [<turn>]")
	}

	if _, err := database.Session(args[0]); err != nil {
		return fmt.Errorf("session %s: %w", args[0], err)
	}

	entries, err := database.Entries(args[0])

	if err != nil {
		return err
	}

	turns := transcript.Summarize(entries).Turns

	if turns == 0 {
		return fmt.Errorf("session %s has no turns yet", args[0])
	}

	turn := turns

	if len(args) == 2 {
		if turn, err = strconv.Atoi(args[1]); err != nil {
			return fmt.Errorf("a turn is a number, counted from 1, not %q", args[1])
		}
	}

	params, sent, err := agent.Request(entries, turn)

	if err != nil {
		return fmt.Errorf("session %s: %w", args[0], err)
	}

	raw, err := json.Marshal(params)

	if err != nil {
		return err
	}

	fmt.Println(string(raw))

	if sent.Version == "" {
		fmt.Fprintf(os.Stderr, "rumbo: turn %d was recorded before rumbo kept the output limit a request set, so the request shows none\n", turn)
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
