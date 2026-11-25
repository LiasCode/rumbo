package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/LiasCode/rumbo/internal/frontend"
	"github.com/LiasCode/rumbo/protocol"
)

// What the host says is read by its shape: a short line ending in a colon
// is a heading, a short label before a colon names a value, what is set in
// is an item with the lines set in under it, and the rest are words.
func TestWhatTheHostSaysIsReadByItsShape(t *testing.T) {
	said := "provider: toy\n" +
		"options chosen in the session: none\n" +
		"the last run did not finish, and can be resumed\n" +
		"the model is told of these skills:\n" +
		"  review: Read a change as a reviewer would\n" +
		"    /home/me/.config/rumbo/skills/review/SKILL.md\n" +
		"left out:\n" +
		"  /home/me/project/.agents/skills/broken\n" +
		"    its SKILL.md has no name"

	want := []part{
		{kind: partField, title: "provider", text: "toy"},
		{kind: partField, title: "options chosen in the session", text: "none"},
		{kind: partParagraph, text: "the last run did not finish, and can be resumed"},
		{kind: partHeading, title: "the model is told of these skills"},
		{kind: partItem, title: "review", text: "Read a change as a reviewer would", details: []string{"/home/me/.config/rumbo/skills/review/SKILL.md"}},
		{kind: partHeading, title: "left out"},
		{kind: partItem, title: "/home/me/project/.agents/skills/broken", details: []string{"its SKILL.md has no name"}},
	}

	if got := outline(said); !reflect.DeepEqual(got, want) {
		t.Fatalf("read as %+v", got)
	}

	// A sentence with a colon in it is no label.
	if got := outline("This is a long sentence that explains a thing. It goes on: and on"); got[0].kind != partParagraph {
		t.Fatalf("a sentence is %+v", got)
	}

	rows := outlined(said, 40)

	for _, row := range rows {
		if visibleWidth(row) > 40 {
			t.Fatalf("a row is wider than the panel: %q", stripped(row))
		}
	}

	if shown := stripped(strings.Join(rows, "\n")); !inOrder(shown, "provider", "toy", "the model is told of these skills", "• review  Read a change", "  /home/me/.config") {
		t.Fatalf("drawn as:\n%s", shown)
	}
}

// scratchRepository is a Git repository in a folder of the test's own,
// with one commit.
func scratchRepository(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"commit", "-qm", "first"}} {
		if args[0] == "add" {
			if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}

		command := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)

		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v, %s", args, err, out)
		}
	}

	return dir
}

// Git says what changed against the last commit, the new files too, and
// the panel shows each file with what was added and taken away.
func TestGitSaysWhatChangedInTheFolder(t *testing.T) {
	dir := scratchRepository(t)

	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("one\n2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "new.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	read, err := readChanges(dir)

	if err != nil {
		t.Fatal(err)
	}

	files := filesOf(read.patch)

	if read.branch != "main" || len(files) != 2 || files[0].path != "notes.txt" || files[0].added != 1 || files[0].removed != 1 || files[1].path != "new.go" || files[1].added != 1 {
		t.Fatalf("read %+v as %+v", read, files)
	}

	p := panels{shown: panelChanges, changed: &read}
	shown := stripped(strings.Join(p.changeRows(defaultTheme, 50), "\n"))

	if !inOrder(shown, "on main · 2 files · +2 −1", "notes.txt", "+1 −1", "@@", "-two", "+2", "new.go", "+package main") || strings.Contains(shown, "diff --git") || strings.Contains(shown, "+++") {
		t.Fatalf("shown:\n%s", shown)
	}

	if _, err := readChanges(t.TempDir()); err == nil || !strings.Contains(err.Error(), "not in a Git repository") {
		t.Fatalf("outside a repository: %v", err)
	}
}

// The sidebar starts put away, and the composer says, beside the model,
// the branch checked out and the folder, under the home directory as ~/.
func TestTheComposerSaysWhereTheSessionIs(t *testing.T) {
	dir := scratchRepository(t)
	t.Setenv("HOME", filepath.Dir(dir))

	s := staged(t, frontend.Handlers{}, protocol.FrontendInitializeParams{Dir: dir, Provider: "toy", Model: "any"})
	s.run(s.a.readBranch())

	if want := "toy · any · ⎇ main · ~/" + filepath.Base(dir); s.a.layout().side != 0 || !strings.Contains(s.screen(), want) {
		t.Fatalf("the screen, without %q:\n%s", want, s.screen())
	}

	if homed("/elsewhere/project") != "/elsewhere/project" || homed(filepath.Dir(dir)) != "~" {
		t.Fatalf("homed %q, %q", homed("/elsewhere/project"), homed(filepath.Dir(dir)))
	}
}
