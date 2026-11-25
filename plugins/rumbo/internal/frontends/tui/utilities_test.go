package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LiasCode/rumbo/protocol"
)

// The last answer is what the model wrote since the last thing that was
// not text, and /copy sends it, or its last code block, to the clipboard.
func TestCopyTakesTheLastAnswer(t *testing.T) {
	s := composing(t)

	if s.a.copyReply(nil) != nil || !strings.Contains(s.screen(), "there is no answer to copy yet") {
		t.Fatalf("copied an answer there is not:\n%s", s.screen())
	}

	for _, event := range []protocol.Event{
		{Type: protocol.EventText, Text: "Looking."},
		{Type: protocol.EventToolStarted, Name: "bash"},
		{Type: protocol.EventToolFinished, Name: "bash", Result: "a.txt"},
		{Type: protocol.EventText, Text: "Here:\n```go\nfmt.Println(1)\n"},
		{Type: protocol.EventTurn, Usage: &protocol.Usage{}},
		{Type: protocol.EventText, Text: "```\nDone.\n"},
	} {
		s.a.shown.apply(event, false, time.Now())
	}

	for args, want := range map[string]string{
		"":     "Here:\n```go\nfmt.Println(1)\n```\nDone.",
		"code": "fmt.Println(1)",
	} {
		s.desktop = nil
		copied := s.a.copyReply(strings.Fields(args))

		if copied == nil {
			t.Fatalf("/copy %s copied nothing", args)
		}

		// What goes to the clipboard is what the program sends the
		// terminal, and the desktop.
		var terminal []string

		for _, msg := range messagesOf(copied) {
			if done, ok := msg.(copiedMsg); ok {
				s.apply(done)
				continue
			}

			terminal = append(terminal, fmt.Sprint(msg))
		}

		if !slices.Equal(terminal, []string{want}) || !slices.Equal(s.desktop, []string{want}) || !strings.Contains(s.screen(), "line(s) to the clipboard") {
			t.Fatalf("/copy %s sent %q to the terminal and %q to the desktop:\n%s", args, terminal, s.desktop, s.screen())
		}
	}

	if s.a.copyReply([]string{"all"}) != nil {
		t.Fatal("/copy all was taken")
	}
}

func TestTheLastCodeBlockIsFound(t *testing.T) {
	for text, want := range map[string]string{
		"```\none\n```\ntext\n~~~sh\ntwo\n~~~": "two",
		"```go\nopen to the end":               "open to the end",
		"````\n```\nnested\n```\n````":         "```\nnested\n```",
	} {
		if got, found := lastCode(text); !found || got != want {
			t.Errorf("lastCode(%q) = %q, %v; want %q", text, got, found, want)
		}
	}

	if _, found := lastCode("no code here"); found {
		t.Fatal("found a code block in plain text")
	}
}

// An export tells the session as a document: what was asked, what the model
// wrote, each call with what it changed or returned, and how runs ended.
func TestAnExportIsTheSessionAsMarkdown(t *testing.T) {
	told := protocol.HistoryResult{Session: "4cf0261c", Events: []protocol.Event{
		{Type: protocol.EventContext, Sections: []string{"environment"}},
		{Type: protocol.EventObjective, Text: "fix the notes"},
		{Type: protocol.EventText, Text: "I will fix "},
		{Type: protocol.EventText, Text: "the **typo**.\n\n"},
		{Type: protocol.EventToolStarted, Name: "edit", Args: json.RawMessage(`{"path":"notes.txt","old":"wrold","new":"world"}`)},
		{Type: protocol.EventToolFinished, Name: "edit", Result: "edited notes.txt (1 replacement(s))"},
		{Type: protocol.EventToolStarted, Name: "bash", Args: json.RawMessage("{\"command\":\"echo `date`\"}")},
		{Type: protocol.EventToolFinished, Name: "bash", Result: "a\n```\nb\n[exit status 1]", Failed: true},
		{Type: protocol.EventTurn, Usage: &protocol.Usage{Input: 1200, Output: 40}},
		{Type: protocol.EventEnded, Status: protocol.RunDone, Took: 4200},
		{Type: protocol.EventObjective, Text: "and the tests"},
		{Type: protocol.EventToolStarted, Name: "bash", Args: json.RawMessage(`{"command":"go test"}`)},
	}}

	document := markdownOf(told, time.Date(2026, 11, 15, 20, 5, 0, 0, time.UTC))

	for _, part := range []string{
		"# Session 4cf0261c\n\nExported from rumbo on 2026-11-15 20:05.\n",
		"\n---\n\n**You**\n\nfix the notes\n",
		"\nfix the notes\n\nI will fix the **typo**.\n\n**✓ edit** `notes.txt`\n\n```diff\n- wrold\n+ world\n```\n",
		"\n**✗ bash** `` echo `date` ``\n\n````text\na\n```\nb\n[exit status 1]\n````\n",
		"\n*✓ done · 4.2s · 1.2k in · 40 out · 0 cached (0%) · 1 turn*\n",
		"\n**· bash** `go test` (it never finished)\n",
	} {
		if !strings.Contains(document, part) {
			t.Fatalf("the export lacks %q:\n%s", part, document)
		}
	}

	if strings.Contains(document, "environment") || strings.Contains(document, escape) {
		t.Fatalf("the export holds what it should not:\n%s", document)
	}
}

// An export never writes over a file.
func TestAnExportLeavesAFileThatExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.md")

	if err := writeNew(path, "first"); err != nil {
		t.Fatal(err)
	}

	if err := writeNew(path, "second"); err == nil || !strings.Contains(err.Error(), "exists, and is left as it is") {
		t.Fatalf("writing over it: %v", err)
	}

	if kept, _ := os.ReadFile(path); string(kept) != "first" {
		t.Fatalf("the file holds %q", kept)
	}
}
