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
	"text/tabwriter"

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
	store.KindProvider:  "A provider is any program that speaks the Agent Provider Protocol on its\nstdin and stdout.",
	store.KindFrontend:  "A front end is any program that speaks the Agent Frontend Protocol on file\ndescriptors 3 (read) and 4 (write). It keeps the terminal on stdin and stdout.",
	store.KindExtension: "An extension is any program that speaks the Agent Extension Protocol on its\nstdin and stdout. The tools it declares are the model's, beside rumbo's own,\nand declaring them adds to every request. One of them takes the place of a\ntool of rumbo's, such as bash, when you say so with replaces. It can also\ndeclare sections, which the model is told when a run starts, and functions,\nwhich a front end offers you as commands named after it.",
}

func programUsage(kind string) string {
	var out strings.Builder

	// A line without a command goes on with what the one before says.
	line := func(command, about string) {
		written := "rumbo " + kind + " " + command

		if command == "" {
			written = ""
		}

		fmt.Fprintln(&out, strings.TrimRight(fmt.Sprintf("  %-50s %s", written, about), " "))
	}

	// There are no extensions but the ones the user added.
	listed, removed := "the "+kindNames[kind]+"s there are, built in and yours", "remove one you added, or what you set for a built-in one"

	if kind == store.KindExtension {
		listed, removed = "the extensions you added", "remove one"
	}

	out.WriteString("usage:\n")
	line("list", listed)

	if kind == store.KindProvider {
		line("show <name>", "start it, and say each setting, where its value comes from,")
		line("", "and what it offers")
		line("models <name>", "the models it offers, asking it")
		line("set <name> <setting>=<value>...", "its model, a field of its configuration, or one of its")
		line("", "options; an empty value gives one back to its default")
	} else if kind == store.KindExtension {
		line("show <name>", "start it, and say what is set for it and the tools it")
		line("", "gives the model")
		line("set <name> <field>=<value>...", "a field of its configuration, or replaces, the tools of")
		line("", "rumbo's own it takes the place of, checked by starting")
		line("", "it; an empty value gives one back")
	} else {
		line("show <name>", "how it is started, and what is set for it")
		line("set <name> <field>=<value>...", "a field of its configuration, checked when it starts;")
		line("", "an empty value gives one back to its default")
	}

	if kind == store.KindExtension {
		line("add [-config <json>] [-replaces <tools>] <name> <command>...", "")
	} else {
		line("add [-config <json>] <name> <command>...", "")
	}

	line("rm <name>", removed)

	fmt.Fprintf(&out, "\n%s\n\"{self}\" as the command stands for the rumbo executable.\n", programHelp[kind])
	fmt.Fprintf(&out, "What you set is kept in %s; what you do not is the %s's own default.", configFile, kindNames[kind])

	return out.String()
}

// retiredCommands are the subcommands there were, and what took their place.
var retiredCommands = map[string]string{
	"config":  "set",
	"options": "set",
}

// programCommands returns the commands for one kind of program.
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
		return printPrograms(database, kind)

	case "add":
		flags := flag.NewFlagSet(kind+" add", flag.ContinueOnError)
		config := flags.String("config", "{}", "")
		replaces := ""

		if kind == store.KindExtension {
			flags.StringVar(&replaces, settingReplaces, "", "")
		}

		if err := flags.Parse(args[1:]); err != nil {
			return err
		}

		rest := flags.Args()

		if len(rest) < 2 {
			return errors.New(help)
		}

		if kind == store.KindExtension {
			added, err := registerExtension(database, rest[0], rest[1:], json.RawMessage(*config), replacing(replaces), os.Stderr)

			if err != nil {
				return err
			}

			fmt.Println(added)

			return nil
		}

		if kind != store.KindProvider {
			return registerProgram(database, kind, rest[0], rest[1:], json.RawMessage(*config))
		}

		return registerProvider(database, rest[0], rest[1:], json.RawMessage(*config), os.Stderr)

	case "set":
		if len(args) < 3 {
			return errors.New(help)
		}

		said, err := setProgram(database, kind, args[1], args[2:], os.Stderr)

		if err != nil {
			return err
		}

		applies := "to the runs that start from now on"

		if kind == store.KindFrontend {
			applies = "from the next start of rumbo, which is when the front end checks it"
		}

		fmt.Printf("%s\nkept in %s; it applies %s\n", strings.Join(said, "\n"), configFile, applies)

		return nil

	case "show":
		if len(args) != 2 {
			return errors.New(help)
		}

		if kind == store.KindProvider {
			shown, err := showProvider(database, args[1], nil, os.Stderr)

			if err != nil {
				return err
			}

			fmt.Println(shown)

			return nil
		}

		if kind == store.KindExtension {
			shown, err := showExtension(database, args[1], os.Stderr)

			if err != nil {
				return err
			}

			fmt.Println(shown)

			return nil
		}

		program, err := findProgram(database, kind, args[1])

		if err != nil {
			return err
		}

		fmt.Printf("%s: %s\nconfiguration: %s\n", program.Name, strings.Join(program.Command, " "), shown(program.Config))

		return nil

	case "models":
		if kind != store.KindProvider || len(args) != 2 {
			return errors.New(help)
		}

		listed, err := listModels(database, args[1], os.Stderr)

		if err != nil {
			return err
		}

		fmt.Println(listed)

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
	}

	if instead, retired := retiredCommands[args[0]]; retired {
		return fmt.Errorf(`"rumbo %s %s" is now "rumbo %s %s <name> <setting>=<value>", which sets anything of a %s`, kind, args[0], kind, instead, kindNames[kind])
	}

	return errors.New(help)
}

// printPrograms lists the programs of a kind there are: how each is
// started, and a provider's model.
func printPrograms(database store.Store, kind string) error {
	programs, err := listPrograms(database, kind)

	if err != nil {
		return err
	}

	out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	for _, program := range programs {
		started := strings.Join(program.Command, " ")

		if _, built := builtin(kind, program.Name); built && program.Command[0] == selfPlaceholder {
			started = sourceBuiltin
		}

		if program.Model == "" {
			fmt.Fprintf(out, "%s\t%s\n", program.Name, started)
			continue
		}

		fmt.Fprintf(out, "%s\t%s\tmodel %s\n", program.Name, started, program.Model)
	}

	return out.Flush()
}

func systemUsage() string {
	kept := configFile

	if path, err := ConfigPath(); err == nil {
		kept = path
	}

	return "usage:\n" +
		"  rumbo system                           every system setting, its value, and where it comes from\n" +
		"  rumbo system set <name>=<value>...     an empty value gives one back to its default\n\n" +
		"System settings are rumbo's own, the same everywhere. What a session chooses\n" +
		"for itself, with -p, -m, -o, use, or session-set, wins over them there.\n" +
		"What you set is kept in " + kept + ", which you can edit;\n" +
		"\"rumbo config\" shows all of it, the providers' and the front ends' too.\n\nsystem settings:\n" + settingsHelp()
}

func systemCommands(database store.Store, args []string) error {
	switch {
	case len(args) == 0:
		settings, err := effectiveSettings(database)

		if err != nil {
			return err
		}

		out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		writeSettings(out, settings, "")

		return out.Flush()

	case args[0] == "set" && len(args) == 3 && !strings.Contains(args[1], "="):
		return fmt.Errorf(`set takes name=value now: "rumbo system set %s=%s"`, args[1], args[2])

	case args[0] == "set" && len(args) > 1:
		said, err := setSettings(database, args[1:])

		if err != nil {
			return err
		}

		fmt.Println(strings.Join(said, "\n"))

		return nil

	case args[0] == "list" || args[0] == "get":
		return fmt.Errorf(`"rumbo system %s" is now "rumbo system", which shows every setting`, args[0])

	case args[0] == "unset":
		return errors.New(`"rumbo system unset <name>" is now "rumbo system set <name>=": an empty value gives a setting back to its default`)
	}

	return errors.New(systemUsage())
}

// configCommand shows what is chosen and what applies, without starting
// anything.
func configCommand(database store.Store, args []string) error {
	if len(args) > 0 {
		return errors.New(`"rumbo config" takes nothing: it shows what is chosen and what applies. "rumbo system set", "rumbo provider set", "rumbo frontend set", and "rumbo extension set" change it`)
	}

	shown, err := describeConfig(database, nil, `"rumbo provider show <name>"`)

	if err != nil {
		return err
	}

	fmt.Println(shown)

	return nil
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
