package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/internal/transcript"
	"github.com/LiasCode/rumbo/protocol"
)

// storedHost is a host on sessions kept in a directory the test can reach.
func storedHost(t *testing.T) (*host, string) {
	t.Helper()

	data := t.TempDir()
	database := store.Open(filepath.Join(t.TempDir(), configFile), data)

	database.PutProgram(store.KindProvider, store.Program{Name: "first", Command: []string{"first-provider"}})

	return hostOn(t, database), data
}

// worked makes a session in the host's directory with the entries given.
func worked(t *testing.T, h *host, title string, entries ...any) store.Session {
	t.Helper()

	session, err := h.database.CreateSession("/repo", title)

	if err != nil {
		t.Fatal(err)
	}

	for index := 0; index < len(entries); index += 2 {
		if _, err := h.database.Append(session.ID, entries[index].(string), entries[index+1]); err != nil {
			t.Fatal(err)
		}
	}

	return session
}

// backdate makes a session look made, and last used, at when.
func backdate(t *testing.T, data, id string, when time.Time) {
	t.Helper()

	dir := filepath.Join(data, "sessions", id)
	path := filepath.Join(dir, "session.json")
	raw, err := os.ReadFile(path)

	if err != nil {
		t.Fatal(err)
	}

	var record map[string]any

	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}

	record["created_at"] = when.UTC().Format(time.RFC3339)
	raw, _ = json.Marshal(record)

	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chtimes(filepath.Join(dir, "entries.jsonl"), when, when); err != nil {
		t.Fatal(err)
	}
}

var (
	asked    = []any{transcript.KindUser, transcript.Text{Text: "go"}}
	answered = []any{transcript.KindAssistant, transcript.Assistant{Usage: protocol.Usage{Input: 3, Output: 7}}}
)

func noted(kind string) []any {
	return []any{transcript.KindNote, transcript.Note{Type: kind}}
}

func steps(parts ...[]any) []any {
	var all []any

	for _, part := range parts {
		all = append(all, part...)
	}

	return all
}

// The list of sessions says how each one stands: how its last run ended,
// how many turns it has, and whether another rumbo process uses it.
func TestSessionsSayHowEachStands(t *testing.T) {
	h, _ := storedHost(t)

	done := worked(t, h, "done", steps(asked, answered, noted(transcript.NoteDone))...)
	failed := worked(t, h, "failed", steps(asked, noted(transcript.NoteFailed))...)
	killed := worked(t, h, "killed", steps(asked, answered, answered)...)
	used := worked(t, h, "used", steps(asked, answered, noted(transcript.NoteDone))...)

	release, err := h.database.LockSession(used.ID)

	if err != nil {
		t.Fatal(err)
	}

	defer release()

	listed, err := standings(h.database, "/repo")

	if err != nil {
		t.Fatal(err)
	}

	stands := map[string]string{}

	for _, each := range listed {
		stands[each.ID] = fmt.Sprintf("%s, %d turns", each.state(), each.summary.Turns)

		if each.ID == used.ID && each.summary.Spent.Output != 7 {
			t.Errorf("the session in use spent %+v", each.summary.Spent)
		}
	}

	for id, want := range map[string]string{
		done.ID:   "done, 1 turns",
		failed.ID: "failed, 0 turns",
		killed.ID: "interrupted, 2 turns",
		used.ID:   "in use, 1 turns",
	} {
		if stands[id] != want {
			t.Errorf("session %s stands as %q, want %q", id, stands[id], want)
		}
	}
}

// A program reads the sessions as JSON: each with its root, how it stands,
// and whether a rumbo holds it, and an empty list where there are none.
func TestSessionsAreListedForAProgram(t *testing.T) {
	h, _ := storedHost(t)

	if out, err := printed(t, func() error { return printSessionsJSON(nil) }); err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("no sessions = %q, err = %v", out, err)
	}

	done := worked(t, h, "done", steps(asked, answered, noted(transcript.NoteDone))...)
	used := worked(t, h, "used", steps(asked, noted(transcript.NoteFailed))...)

	release, err := h.database.LockSession(used.ID)

	if err != nil {
		t.Fatal(err)
	}

	defer release()

	out, err := printed(t, func() error { return printSessions(h.database, []string{"-all", "-json"}) })

	if err != nil {
		t.Fatal(err)
	}

	var listed []listedSession

	if err := json.Unmarshal([]byte(out), &listed); err != nil || len(listed) != 2 {
		t.Fatalf("listed %q, err = %v", out, err)
	}

	stands := map[string]listedSession{}

	for _, each := range listed {
		stands[each.Session] = each
	}

	if got := stands[done.ID]; got.Title != "done" || got.Dir != "/repo" || got.State != "done" || got.InUse || got.Turns != 1 || got.Used.IsZero() {
		t.Errorf("the session that is done: %+v", got)
	}

	if got := stands[used.ID]; got.State != "failed" || !got.InUse {
		t.Errorf("the session in use: %+v", got)
	}
}

// The session can be given a title of the person's own, which it keeps.
func TestRenameGivesTheSessionATitle(t *testing.T) {
	h, _ := storedHost(t)

	if err := refused(t, h, "rename", `{"title":"early"}`); !strings.Contains(err, "no task yet") {
		t.Fatalf("renaming a session still to be made: %s", err)
	}

	session := worked(t, h, "fix the parser", steps(asked)...)
	h.session = &session

	if renamed := call(t, h, "rename", `{"title":"  Parser   rewrite "}`); !strings.Contains(renamed.Text, `"Parser rewrite"`) {
		t.Fatalf("rename answered %q", renamed.Text)
	}

	if stored, _ := h.database.Session(session.ID); stored.Title != "Parser rewrite" {
		t.Fatalf("the stored title is %q", stored.Title)
	}

	if status := call(t, h, "status", ""); !strings.Contains(status.Text, "(Parser rewrite)") {
		t.Fatalf("status after renaming:\n%s", status.Text)
	}

	if err := refused(t, h, "rename", `{"title":"   "}`); !strings.Contains(err, "needs some text") {
		t.Fatalf("an empty title: %s", err)
	}
}

// printed runs a command and returns what it wrote to stdout.
func printed(t *testing.T, command func() error) (string, error) {
	t.Helper()

	read, write, err := os.Pipe()

	if err != nil {
		t.Fatal(err)
	}

	stdout := os.Stdout
	os.Stdout = write
	failed := command()
	os.Stdout = stdout
	write.Close()

	out, err := io.ReadAll(read)

	if err != nil {
		t.Fatal(err)
	}

	return string(out), failed
}

// Pruning lists the sessions not used in a while, and deletes them with
// -yes, keeping those a rumbo process is using, however old.
func TestPruneDeletesWhatWasNotUsed(t *testing.T) {
	h, data := storedHost(t)
	old := time.Now().Add(-40 * 24 * time.Hour)

	forgotten := worked(t, h, "forgotten", steps(asked)...)
	elsewhere := worked(t, h, "elsewhere", steps(asked)...)
	recent := worked(t, h, "recent", steps(asked)...)

	for _, session := range []store.Session{forgotten, elsewhere} {
		backdate(t, data, session.ID, old)
	}

	release, err := h.database.LockSession(elsewhere.ID)

	if err != nil {
		t.Fatal(err)
	}

	defer release()

	prune := func(args ...string) (string, error) {
		return printed(t, func() error { return pruneSessions(h.database, append([]string{"-all"}, args...)) })
	}

	if _, err := prune("30"); err == nil || !strings.Contains(err.Error(), "such as 12h, 30d, or 2w") {
		t.Fatalf("an age without a unit: %v", err)
	}

	listed, err := prune("30d")

	if err != nil || !strings.HasPrefix(listed, "2 sessions were last used more than 30d ago:") || !strings.Contains(listed, forgotten.ID+"  ") || strings.Contains(listed, recent.ID) {
		t.Fatalf("prune without -yes printed, err = %v:\n%s", err, listed)
	}

	if _, err := h.database.Session(forgotten.ID); err != nil {
		t.Fatalf("prune without -yes deleted a session: %v", err)
	}

	if none, err := prune("-yes", "60d"); err != nil || none != "no session was last used more than 60d ago\n" {
		t.Fatalf("a prune with nothing to delete printed, err = %v: %s", err, none)
	}

	// The one in use is kept, which the exit code says.
	pruned, err := prune("-yes", "30d")

	if _, failed := errors.AsType[exitCode](err); !failed || pruned != "deleted session "+forgotten.ID+"\n" {
		t.Fatalf("prune printed, err = %v:\n%s", err, pruned)
	}

	for _, session := range []store.Session{elsewhere, recent} {
		if _, err := h.database.Session(session.ID); err != nil {
			t.Errorf("%s was not kept: %v", session.Title, err)
		}
	}

	if _, err := h.database.Session(forgotten.ID); err == nil {
		t.Error("the forgotten session is still there")
	}
}

// An age is a count of hours, days, or weeks.
func TestAgesAreHoursDaysOrWeeks(t *testing.T) {
	for text, want := range map[string]time.Duration{"12h": 12 * time.Hour, "30d": 30 * 24 * time.Hour, "2w": 14 * 24 * time.Hour} {
		if got, err := parseAge(text); err != nil || got != want {
			t.Errorf("%s is %v, %v; want %v", text, got, err, want)
		}
	}

	for _, text := range []string{"", "30", "d", "0d", "-1d", "1y", "1.5d"} {
		if _, err := parseAge(text); err == nil {
			t.Errorf("%q was taken for an age", text)
		}
	}
}

// A session that another rumbo process deleted while this host had it,
// without working in it, takes no run, and the person is told why.
func TestASessionDeletedElsewhereTakesNoRun(t *testing.T) {
	h, _ := storedHost(t)
	session := worked(t, h, "opened", steps(asked, answered, noted(transcript.NoteDone))...)

	// As rumbo -s starts with it: not held until it works.
	h.session = &session

	if err := discard(h.database, session.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := h.accept(protocol.RunParams{Objective: "go on"}, nil); err == nil || !strings.Contains(err.Error(), "was deleted by another rumbo process") {
		t.Fatalf("a run in a deleted session: err = %v", err)
	}
}
