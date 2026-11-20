package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/internal/transcript"
	"github.com/LiasCode/rumbo/protocol"
)

// storedHost is a host whose sessions are in a directory the test can reach.
func storedHost(t *testing.T) (*host, string) {
	t.Helper()

	data := t.TempDir()
	database := store.Open(filepath.Join(t.TempDir(), configFile), data)

	database.PutProgram(store.KindProvider, store.Program{Name: "first", Command: []string{"first-provider"}})

	return hostOn(t, database), data
}

// worked makes a session of the host's project with the entries given.
func worked(t *testing.T, h *host, title string, entries ...any) store.Session {
	t.Helper()

	session, err := h.database.CreateSession("/repo", "/repo", title)

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

	described := map[string]string{}

	for _, value := range call(t, h, "sessions", "").Values {
		described[value.Value] = value.Description
	}

	for id, want := range map[string]string{
		done.ID:   "done; 1 turn; last used ",
		failed.ID: "failed; 0 turns; last used ",
		killed.ID: "interrupted; 2 turns; last used ",
		used.ID:   "in use by another rumbo process; 1 turn; last used ",
	} {
		if !strings.HasPrefix(described[id], want) {
			t.Errorf("session %s is described %q, want it to start %q", id, described[id], want)
		}
	}

	listed, err := standings(h.database, "/repo")

	if err != nil {
		t.Fatal(err)
	}

	for _, each := range listed {
		if each.ID == used.ID && (each.state() != "in use" || each.summary.Spent.Output != 7) {
			t.Errorf("the session in use stands as %q, having spent %+v", each.state(), each.summary.Spent)
		}
	}
}

// The current session can be given a title of the person's own, which it
// keeps.
func TestRenameGivesTheCurrentSessionATitle(t *testing.T) {
	h, _ := storedHost(t)

	if err := refused(t, h, "rename", `{"title":"early"}`); !strings.Contains(err, "no task yet") {
		t.Fatalf("renaming a session still to be made: %s", err)
	}

	session := worked(t, h, "fix the parser", steps(asked)...)
	call(t, h, "open", `{"session":"`+session.ID+`"}`)

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

// Pruning lists the project's sessions not used in a while, and deletes them
// when confirmed, keeping the current one and those in use, however old.
func TestPruneDeletesWhatWasNotUsed(t *testing.T) {
	h, data := storedHost(t)
	old := time.Now().Add(-40 * 24 * time.Hour)

	forgotten := worked(t, h, "forgotten", steps(asked)...)
	elsewhere := worked(t, h, "elsewhere", steps(asked)...)
	current := worked(t, h, "current", steps(asked)...)
	recent := worked(t, h, "recent", steps(asked)...)

	for _, session := range []store.Session{forgotten, elsewhere, current} {
		backdate(t, data, session.ID, old)
	}

	call(t, h, "open", `{"session":"`+current.ID+`"}`)

	release, err := h.database.LockSession(elsewhere.ID)

	if err != nil {
		t.Fatal(err)
	}

	defer release()

	if err := refused(t, h, "prune", `{"age":"30"}`); !strings.Contains(err, "such as 12h, 30d, or 2w") {
		t.Fatalf("an age without a unit: %s", err)
	}

	if err := refused(t, h, "prune", `{"age":"30d","confirm":"no"}`); !strings.Contains(err, "confirm is yes") {
		t.Fatalf("a confirm other than yes: %s", err)
	}

	listed := call(t, h, "prune", `{"age":"30d"}`).Text

	if !strings.HasPrefix(listed, "3 sessions of this project last used more than 30d ago:") ||
		strings.Count(listed, "(in use, kept)") != 2 || !strings.Contains(listed, forgotten.ID+"  ") || strings.Contains(listed, recent.ID) {
		t.Fatalf("prune without confirm answered:\n%s", listed)
	}

	if _, err := h.database.Session(forgotten.ID); err != nil {
		t.Fatalf("prune without confirm deleted a session: %v", err)
	}

	if none := call(t, h, "prune", `{"age":"60d","confirm":"yes"}`).Text; none != "no session of this project was last used more than 60d ago" {
		t.Fatalf("a prune with nothing to delete answered: %s", none)
	}

	pruned := call(t, h, "prune", `{"age":"30d","confirm":"yes"}`).Text

	if !strings.HasPrefix(pruned, "deleted 1 session last used more than 30d ago\n  "+forgotten.ID) || !strings.Contains(pruned, "kept 2 sessions in use") {
		t.Fatalf("prune answered:\n%s", pruned)
	}

	for _, session := range []store.Session{elsewhere, current, recent} {
		if _, err := h.database.Session(session.ID); err != nil {
			t.Errorf("%s was not kept: %v", session.Title, err)
		}
	}

	if _, err := h.database.Session(forgotten.ID); err == nil {
		t.Error("the forgotten session is still there")
	}

	if again := call(t, h, "prune", `{"age":"30d","confirm":"yes"}`).Text; !strings.HasPrefix(again, "deleted 0 sessions") {
		t.Fatalf("a second prune answered:\n%s", again)
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

// A session that another rumbo process deleted while this host had it open,
// without working in it, takes no run, and the person is told why.
func TestASessionDeletedElsewhereTakesNoRun(t *testing.T) {
	h, _ := storedHost(t)
	session := worked(t, h, "opened", steps(asked, answered, noted(transcript.NoteDone))...)

	// Opened as rumbo -s opens it: current, and not held until it works.
	h.current = &place{choice: h.current.choice, session: &session, dir: "/repo", tools: h.tools.Separate()}
	h.places[session.ID] = h.current

	if err := discard(h.database, session.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := h.accept(protocol.RunParams{Objective: "go on"}, nil); err == nil || !strings.Contains(err.Error(), "was deleted by another rumbo process") {
		t.Fatalf("a run in a deleted session: err = %v", err)
	}
}
