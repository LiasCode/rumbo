package tui

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LiasCode/rumbo/protocol"
)

// The last answer is what the model wrote since the last thing that was
// not text, and /copy sends it, or its last code block, to the clipboard.
func TestCopyTakesTheLastAnswer(t *testing.T) {
	u := quiet()
	run := &job{}

	if _, err := u.copyAnswer(nil); err == nil {
		t.Fatal("copied an answer there is not")
	}

	for _, event := range []protocol.Event{
		{Type: protocol.EventText, Text: "Looking."},
		{Type: protocol.EventToolStarted, Name: "bash"},
		{Type: protocol.EventToolFinished, Name: "bash", Result: "a.txt"},
		{Type: protocol.EventText, Text: "Here:\n```go\nfmt.Println(1)\n"},
		{Type: protocol.EventTurn, Usage: &protocol.Usage{}},
		{Type: protocol.EventText, Text: "```\nDone.\n"},
	} {
		u.tell(run, event)
	}

	for args, want := range map[string]string{
		"":     "Here:\n```go\nfmt.Println(1)\n```\nDone.",
		"code": "fmt.Println(1)",
	} {
		written := u.screen.out.(*bytes.Buffer)
		written.Reset()

		copied, err := u.copyAnswer(strings.Fields(args))
		sent := escape + "]52;c;" + base64.StdEncoding.EncodeToString([]byte(want)) + "\a"

		if err != nil || !strings.Contains(written.String(), sent) || !strings.Contains(copied.text, "line(s) to the clipboard") {
			t.Fatalf("/copy %s: %q, %v; wrote %q", args, copied.text, err, written.String())
		}
	}

	if _, err := u.copyAnswer([]string{"all"}); err == nil {
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
		{Type: protocol.EventEnded, Status: protocol.RunDone},
		{Type: protocol.EventObjective, Text: "and the tests"},
		{Type: protocol.EventToolStarted, Name: "bash", Args: json.RawMessage(`{"command":"go test"}`)},
	}}

	document := markdownOf(told, time.Date(2026, 11, 15, 20, 5, 0, 0, time.UTC))

	for _, part := range []string{
		"# Session 4cf0261c\n\nExported from rumbo on 2026-11-15 20:05.\n",
		"\n---\n\n**You**\n\nfix the notes\n",
		"\nfix the notes\n\nI will fix the **typo**.\n\n**✓ edit** `notes.txt`\n\n```diff\n- wrold\n+ world\n```\n",
		"\n**✗ bash** `` echo `date` ``\n\n````text\na\n```\nb\n[exit status 1]\n````\n",
		"\n*✓ done · 1 turn · 1.2k in, 0 cached, 40 out*\n",
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
