package tui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/LiasCode/rumbo/internal/frontend"
	"github.com/LiasCode/rumbo/protocol"
)

// pngOf is a PNG of width×height.
func pngOf(t *testing.T, width, height int) []byte {
	t.Helper()

	var buffer bytes.Buffer

	if err := png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}

	return buffer.Bytes()
}

// holding is a clipboard that holds data, or fails with err.
func holding(data []byte, err error) func(context.Context) (held, error) {
	return func(context.Context) (held, error) {
		return held{image: data}, err
	}
}

// cached keeps what a test pastes in a directory of its own, and returns
// it.
func cached(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("RUMBO_HOME", dir)

	return dir
}

// Ctrl-V saves the clipboard's image as a file and puts it in the composer,
// which shows it above what is typed; Backspace on an empty composer takes
// the last away, and Ctrl-C all of them.
func TestCtrlVPastesTheClipboardsImage(t *testing.T) {
	cache := cached(t)
	s := composing(t)
	s.a.clipboard = holding(pngOf(t, 40, 20), nil)

	s.apply(ctrl('v'))
	s.a.clipboard = holding(pngOf(t, 8, 6), nil)
	s.apply(ctrl('v'))

	if len(s.a.files) != 2 || s.a.files[0] == s.a.files[1] || strings.Count(s.screen(), "📷 pasted-") != 2 {
		t.Fatalf("pasted the files %q:\n%s", s.a.files, s.screen())
	}

	if kept, err := os.ReadFile(s.a.files[0]); err != nil || !bytes.Equal(kept, pngOf(t, 40, 20)) || filepath.Dir(s.a.files[0]) != filepath.Join(cache, "pasted") || filepath.Ext(s.a.files[0]) != ".png" {
		t.Fatalf("the first image was kept in %s: %d bytes, %v", s.a.files[0], len(kept), err)
	}

	s.text("ab")
	s.press(tea.KeyBackspace)

	if len(s.a.files) != 2 || s.a.input.String() != "a" {
		t.Fatalf("Backspace on a line took %d file(s) and left %q", 2-len(s.a.files), s.a.input.String())
	}

	s.press(tea.KeyBackspace)
	s.press(tea.KeyBackspace)

	if len(s.a.files) != 1 || !s.a.input.empty() {
		t.Fatalf("Backspace on an empty composer left %d file(s)", len(s.a.files))
	}

	s.apply(ctrl('c'))

	if len(s.a.files) != 0 {
		t.Fatal("Ctrl-C left the files")
	}
}

// What the clipboard holds that is no image is said, and nothing is put in
// the composer.
func TestAClipboardWithoutAnImageIsSaid(t *testing.T) {
	for _, held := range []struct {
		data []byte
		err  error
		said string
	}{
		{nil, errNothingHeld, "holds no file and no image"},
		{[]byte("just text"), nil, "not a PNG, JPEG, GIF, or WebP"},
		{nil, errors.New("no access"), "no access"},
	} {
		s := composing(t)
		s.a.clipboard = holding(held.data, held.err)

		s.apply(ctrl('v'))

		if len(s.a.files) != 0 || !strings.Contains(s.a.notice, held.said) {
			t.Errorf("pasted %d, notice %q; want it to say %q", len(s.a.files), s.a.notice, held.said)
		}
	}
}

// What the clipboard program lists is looked through for an image a model
// takes, and that one is read.
func TestTheClipboardIsReadForAnImage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("rumbo does not read the clipboard on Windows")
	}

	dir := t.TempDir()
	file := filepath.Join(dir, "image.png")
	os.WriteFile(file, pngOf(t, 4, 2), 0o644)
	program := "#!/bin/sh\ncase \"$1\" in\n--list-types) printf 'text/plain\\nimage/bmp\\nimage/png\\n';;\n*) [ \"$3\" = image/png ] && cat " + file + ";;\nesac\n"
	os.WriteFile(filepath.Join(dir, "wl-paste"), []byte(program), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	list := []string{"--list-types"}
	get := func(kind string) []string { return []string{"--no-newline", "--type", kind} }
	data, err := listedClipboard(context.Background(), "wl-paste", "wl-clipboard", list, get)

	if err != nil || !bytes.Equal(data.image, pngOf(t, 4, 2)) {
		t.Fatalf("read %d bytes, %v", len(data.image), err)
	}

	os.WriteFile(filepath.Join(dir, "wl-paste"), []byte("#!/bin/sh\necho text/plain\n"), 0o755)

	if _, err := listedClipboard(context.Background(), "wl-paste", "wl-clipboard", list, get); !errors.Is(err, errNothingHeld) {
		t.Fatalf("a clipboard of text: %v", err)
	}

	if _, err := listedClipboard(context.Background(), "no-such-clipboard", "it", list, get); err == nil || !strings.Contains(err.Error(), "is not installed") {
		t.Fatalf("a program that is not there: %v", err)
	}
}

// Enter sends the files with what is typed, or alone, as a task, by their
// paths, and the conversation shows them under it; the history shows them
// where they were given.
func TestFilesGoWithTheTask(t *testing.T) {
	cached(t)
	given := make(chan protocol.RunParams, 2)

	s := staged(t, frontend.Handlers{Run: func(params protocol.RunParams, run *frontend.Run) (frontend.Work, error) {
		given <- params

		return func(context.Context) (string, error) { return "s1", nil }, nil
	}}, protocol.FrontendInitializeParams{Dir: "/home/me/alpha", Provider: "toy", Model: "any"})

	s.a.clipboard = holding(pngOf(t, 40, 20), nil)

	s.apply(ctrl('v'))
	s.typing("what is this?")

	sent := <-given

	if sent.Objective != "what is this?" || len(sent.Files) != 1 || !filepath.IsAbs(sent.Files[0]) || len(s.a.files) != 0 {
		t.Fatalf("sent %q with the files %q; %d left in the composer", sent.Objective, sent.Files, len(s.a.files))
	}

	if screen := s.screen(); !inOrder(screen, "what is this?", "📷 "+filepath.Base(sent.Files[0])) {
		t.Fatalf("the conversation shows:\n%s", screen)
	}

	s.apply(ctrl('v'))
	s.press(tea.KeyEnter)

	if alone := <-given; alone.Objective != "" || len(alone.Files) != 1 {
		t.Fatalf("a file alone was sent as %q with the files %q", alone.Objective, alone.Files)
	}

	s.a.shown.apply(protocol.Event{Type: protocol.EventObjective, Text: "and this?", Files: []string{"/srv/reports/sales.pdf"}}, true, time.Now())

	if screen := s.screen(); !inOrder(screen, "and this?", "📕 sales.pdf") {
		t.Fatalf("the history shows:\n%s", screen)
	}
}

// /attach puts a file in the composer by its path, from the session's
// folder, as it was typed, and says so when there is none there.
func TestAttachGivesAFileByItsPath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "plan one.pdf"), []byte("%PDF"), 0o644)

	s := staged(t, frontend.Handlers{}, protocol.FrontendInitializeParams{Dir: dir, Provider: "toy", Model: "any"})

	s.typing("/attach plan one.pdf")
	s.typing("/attach missing.csv")

	if len(s.a.files) != 1 || s.a.files[0] != "plan one.pdf" {
		t.Fatalf("attached the files %q", s.a.files)
	}

	// A long path goes on to the next row, so the rows are read as one.
	if screen := strings.Join(strings.Fields(s.screen()), " "); !strings.Contains(screen, "📕 plan one.pdf") || !strings.Contains(screen, "there is no file at "+filepath.Join(dir, "missing.csv")) {
		t.Fatalf("the screen shows:\n%s", screen)
	}
}

// The files copied in a file manager are in the clipboard as a list of
// addresses, and Ctrl-V gives them as they are, where they are, before
// the image one of them may also be there as.
func TestCtrlVGivesTheFilesTheClipboardHolds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("rumbo does not read the clipboard on Windows")
	}

	dir := t.TempDir()
	program := "#!/bin/sh\ncase \"$1\" in\n--list-types) printf 'image/png\\ntext/uri-list\\n';;\n*) [ \"$3\" = text/uri-list ] && printf 'file:///srv/my%%20notes/plan.pdf\\r\\nfile:///srv/data.bin\\r\\n';;\nesac\n"
	os.WriteFile(filepath.Join(dir, "wl-paste"), []byte(program), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	found, err := listedClipboard(context.Background(), "wl-paste", "wl-clipboard", []string{"--list-types"}, func(kind string) []string {
		return []string{"--no-newline", "--type", kind}
	})

	if err != nil || !slices.Equal(found.files, []string{"/srv/my notes/plan.pdf", "/srv/data.bin"}) {
		t.Fatalf("the clipboard gave the files %q, %v", found.files, err)
	}

	s := composing(t)
	s.a.clipboard = func(context.Context) (held, error) { return found, nil }

	s.apply(ctrl('v'))

	if screen := s.screen(); len(s.a.files) != 2 || !strings.Contains(screen, "📕 plan.pdf  📄 data.bin") {
		t.Fatalf("pasted the files %q:\n%s", s.a.files, screen)
	}
}

// A file dropped on the terminal is pasted as its path, quoted or not, and
// is given as a file; a paste that is anything else is text.
func TestAFileDroppedOnTheTerminalIsGiven(t *testing.T) {
	dir := t.TempDir()
	plain, spaced := filepath.Join(dir, "sales.csv"), filepath.Join(dir, "my notes.md")
	os.WriteFile(plain, nil, 0o644)
	os.WriteFile(spaced, nil, 0o644)

	type paste struct {
		text  string
		files []string
	}

	pastes := []paste{
		{plain, []string{plain}},
		{spaced + "\n", []string{spaced}},
		{"'" + spaced + "' " + plain, []string{spaced, plain}},
		{`"` + spaced + `" ` + plain, []string{spaced, plain}},
		{"look at " + plain, nil},
		{filepath.Join(dir, "missing.txt"), nil},
		{"sales.csv", nil},
		{"", nil},
	}

	// On Windows a backslash separates folders, and an address is
	// file:///C:/...; elsewhere a backslash escapes what follows it.
	if runtime.GOOS == "windows" {
		pastes = append(pastes, paste{"file:///" + filepath.ToSlash(plain) + "\r\n" + plain, []string{plain, plain}})
	} else {
		pastes = append(pastes,
			paste{strings.ReplaceAll(spaced, " ", "\\ "), []string{spaced}},
			paste{"file://" + plain + "\r\n" + plain, []string{plain, plain}})
	}

	for _, pasted := range pastes {
		s := composing(t)

		s.apply(tea.PasteMsg{Content: pasted.text})

		if !slices.Equal(s.a.files, pasted.files) || s.a.input.empty() != (pasted.files != nil || pasted.text == "") {
			t.Errorf("pasting %q gave the files %q and typed %q", pasted.text, s.a.files, s.a.input.String())
		}
	}
}

// A file shows after the icon of its kind, which the end of its name says,
// and after that of any file when it says nothing.
func TestAFileShowsWithTheIconOfItsKind(t *testing.T) {
	if shown := fileLabels([]string{"/a/shot.PNG", "report.pdf", "data.csv", "main.go", "Makefile", "x.unknown"}); shown != "📷 shot.PNG  📕 report.pdf  📊 data.csv  📜 main.go  📄 Makefile  📄 x.unknown" {
		t.Fatalf("the files show as %q", shown)
	}

	for kind, each := range icons {
		if visibleWidth(each) != 2 {
			t.Errorf("the icon of %s is %d columns wide", kind, visibleWidth(each))
		}
	}
}

// hosted returns a front end's connection to a host that handlers answer,
// which tells the front end info when it starts.
func hosted(t *testing.T, handlers frontend.Handlers, info protocol.FrontendInitializeParams) *protocol.FrontendConn {
	t.Helper()

	conn, _ := hostedUntil(t, handlers, info)

	return conn
}

// hostedUntil is hosted, with what closes the connection, as ending the
// host does.
func hostedUntil(t *testing.T, handlers frontend.Handlers, info protocol.FrontendInitializeParams) (*protocol.FrontendConn, func()) {
	t.Helper()

	hostReads, frontendWrites := io.Pipe()
	frontendReads, hostWrites := io.Pipe()
	dialed := make(chan *protocol.FrontendConn, 1)

	go func() {
		conn, _, err := protocol.DialFrontend(frontendReads, frontendWrites, "test")

		if err != nil {
			t.Error(err)
		}

		dialed <- conn
	}()

	host, err := frontend.Connect(hostReads, hostWrites, info)

	if err != nil {
		t.Fatal(err)
	}

	go host.Serve(handlers)
	t.Cleanup(func() { frontendWrites.Close() })

	return <-dialed, func() { frontendWrites.Close() }
}

// An export names the files a person gave, under their words.
func TestAnExportNamesTheFilesGiven(t *testing.T) {
	told := protocol.HistoryResult{Session: "s", Events: []protocol.Event{
		{Type: protocol.EventObjective, Text: "what is this?", Files: []string{"shots/login.png"}},
	}}

	if document := markdownOf(told, time.Now()); !strings.Contains(document, "**You**\n\nwhat is this?\n\n*📷 login.png*\n") {
		t.Fatalf("the export:\n%s", document)
	}
}
