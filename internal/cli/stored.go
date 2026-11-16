package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/LiasCode/rumbo/internal/frontends/render"
	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/internal/transcript"
)

// This file owns what a person does with stored sessions besides working
// in them: listing them with how each one stands, renaming one, and
// deleting them, one by one or by age. The command line and the host's
// functions both go through it.

const sessionsUsage = `usage:
  rumbo sessions [-all]                     list the sessions of this project, or of every one
  rumbo sessions rename <session> <title>   give a session a title of your own
  rumbo sessions rm <session>...            delete sessions and their history
  rumbo sessions prune [-all] [-yes] <age>  delete the sessions not used in that long, such as
                                            12h, 30d, or 2w; without -yes, only list them

Sessions in use by a rumbo process are not deleted.`

// standing is a stored session and what its entries add up to.
type standing struct {
	store.Session
	summary transcript.Summary
	// inUse says that a rumbo process holds the session.
	inUse bool
}

// standings returns the sessions of project, or of every project when it is
// empty, most recently used first, with how each one stands.
func standings(database store.Store, project string) ([]standing, error) {
	sessions, err := database.Sessions(project)

	if err != nil {
		return nil, err
	}

	listed := make([]standing, 0, len(sessions))

	for _, session := range sessions {
		entries, err := database.Entries(session.ID)

		if err != nil {
			return nil, err
		}

		used, err := database.SessionInUse(session.ID)

		if err != nil {
			return nil, err
		}

		listed = append(listed, standing{Session: session, summary: transcript.Summarize(entries), inUse: used})
	}

	return listed, nil
}

// ended says how the session's last run ended: done, failed, cancelled, or
// interrupted, when it has no ending because its process died. A run that
// works has none either, and its session is held by the process.
func (s standing) ended() string {
	switch {
	case s.summary.Runs == 0:
		return "new"
	case s.summary.Ended == "":
		return "interrupted"
	}

	return s.summary.Ended
}

// state is how the session stands for a listing.
func (s standing) state() string {
	if s.inUse {
		return "in use"
	}

	return s.ended()
}

// retitle gives a session the title a person chose, on one line.
func retitle(database store.Store, id, title string) (string, error) {
	title = strings.Join(strings.Fields(title), " ")

	if title == "" {
		return "", errors.New("a title needs some text")
	}

	if err := database.RenameSession(id, title); err != nil {
		return "", fmt.Errorf("session %s: %w", id, err)
	}

	return title, nil
}

// discard deletes a session that no process holds, holding it meanwhile so
// that no rumbo starts to work in it.
func discard(database store.Store, id string) error {
	if _, err := database.Session(id); err != nil {
		return fmt.Errorf("session %s: %w", id, err)
	}

	release, err := database.LockSession(id)

	if err != nil {
		return fmt.Errorf("session %s: %w", id, err)
	}

	defer release()

	return database.DeleteSession(id)
}

// parseAge reads how long ago a session was last used, in hours, days, or
// weeks.
func parseAge(text string) (time.Duration, error) {
	units := map[byte]time.Duration{'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour}

	if len(text) >= 2 {
		if unit, ok := units[text[len(text)-1]]; ok {
			if count, err := strconv.Atoi(text[:len(text)-1]); err == nil && count > 0 {
				return time.Duration(count) * unit, nil
			}
		}
	}

	return 0, fmt.Errorf("an age is a number of hours, days, or weeks, such as 12h, 30d, or 2w, not %q", text)
}

// unused returns the sessions last used before cutoff.
func unused(sessions []store.Session, cutoff time.Time) []store.Session {
	var old []store.Session

	for _, session := range sessions {
		if session.ActiveAt.Before(cutoff) {
			old = append(old, session)
		}
	}

	return old
}

// titled is a session's title, with the session a branch came from.
func titled(session store.Session) string {
	title := session.Title

	if session.Parent != "" {
		title += "  (branch of " + session.Parent + ")"
	}

	return title
}

// sessionCommands are rumbo sessions and what follows it.
func sessionCommands(database store.Store, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "rename":
			if len(args) < 3 {
				return errors.New(sessionsUsage)
			}

			title, err := retitle(database, args[1], strings.Join(args[2:], " "))

			if err == nil {
				fmt.Printf("session %s is now %q\n", args[1], title)
			}

			return err

		case "rm":
			return removeSessions(database, args[1:])

		case "prune":
			return pruneSessions(database, args[1:])
		}
	}

	return printSessions(database, args)
}

// printSessions lists sessions with how each one stands.
func printSessions(database store.Store, args []string) error {
	flags := flag.NewFlagSet("sessions", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	all := flags.Bool("all", false, "")

	if err := flags.Parse(args); err != nil || flags.NArg() > 0 {
		return errors.New(sessionsUsage)
	}

	project, err := chosenProject(*all)

	if err != nil {
		return err
	}

	listed, err := standings(database, project)

	if err != nil {
		return err
	}

	if len(listed) == 0 {
		fmt.Println("no sessions yet")
		return nil
	}

	out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	where := ""

	if *all {
		where = "PROJECT\t"
	}

	fmt.Fprintf(out, "SESSION\tLAST USED\tSTATE\tTURNS\tIN\tFROM CACHE\tOUT\t%sTITLE\n", where)

	for _, each := range listed {
		if *all {
			where = each.Project + "\t"
		}

		spent := each.summary.Spent

		fmt.Fprintf(out, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%s%s\n",
			each.ID, each.ActiveAt.Format("2006-01-02 15:04"), each.state(), each.summary.Turns,
			render.Input(spent), spent.CacheRead, spent.Output, where, titled(each.Session))
	}

	return out.Flush()
}

// chosenProject is the project the command acts on: the working one, or
// every one, which is the empty project.
func chosenProject(all bool) (string, error) {
	if all {
		return "", nil
	}

	_, root, err := workingProject()

	return root, err
}

// removeSessions deletes the sessions named, and says which it could not.
func removeSessions(database store.Store, ids []string) error {
	if len(ids) == 0 {
		return errors.New(sessionsUsage)
	}

	failed := 0

	for _, id := range ids {
		if err := discard(database, id); err != nil {
			fmt.Fprintln(os.Stderr, "rumbo:", err)
			failed++

			continue
		}

		fmt.Printf("deleted session %s\n", id)
	}

	if failed > 0 {
		return exitCode(1)
	}

	return nil
}

// pruneSessions deletes the sessions not used in a while, or only says which
// they are without -yes.
func pruneSessions(database store.Store, args []string) error {
	flags := flag.NewFlagSet("sessions prune", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	all := flags.Bool("all", false, "")
	yes := flags.Bool("yes", false, "")

	if err := flags.Parse(args); err != nil || flags.NArg() != 1 {
		return errors.New(sessionsUsage)
	}

	age, err := parseAge(flags.Arg(0))

	if err != nil {
		return err
	}

	project, err := chosenProject(*all)

	if err != nil {
		return err
	}

	sessions, err := database.Sessions(project)

	if err != nil {
		return err
	}

	old := unused(sessions, time.Now().Add(-age))

	if len(old) == 0 {
		fmt.Printf("no session was last used more than %s ago\n", flags.Arg(0))
		return nil
	}

	if !*yes {
		fmt.Printf("%d sessions were last used more than %s ago:\n", len(old), flags.Arg(0))

		for _, session := range old {
			fmt.Printf("  %s  %s  %s\n", session.ID, session.ActiveAt.Format("2006-01-02 15:04"), titled(session))
		}

		fmt.Println("run it again with -yes to delete them, with their history")

		return nil
	}

	return removeSessions(database, ids(old))
}

func ids(sessions []store.Session) []string {
	listed := make([]string, len(sessions))

	for index, session := range sessions {
		listed[index] = session.ID
	}

	return listed
}
