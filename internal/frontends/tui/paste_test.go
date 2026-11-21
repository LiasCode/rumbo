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
	"strings"
	"testing"
	"time"

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
func holding(data []byte, err error) func(context.Context) ([]byte, error) {
	return func(context.Context) ([]byte, error) {
		return data, err
	}
}

// Ctrl-V puts the clipboard's image in the input, which shows it above
// what is typed; Backspace on an empty line takes the last away, and
// Ctrl-C all of them.
func TestCtrlVPastesTheClipboardsImage(t *testing.T) {
	u := quiet()
	u.clipboard = holding(pngOf(t, 40, 20), nil)

	u.press(key{r: 0x16})
	u.press(key{r: 0x16})
	u.wait.Wait()

	if len(u.pasted) != 2 {
		t.Fatalf("pasted %d image(s)", len(u.pasted))
	}

	u.footer()

	if shown := drawn(u); !strings.Contains(shown, "[image 1: png, 40×20]  [image 2: png, 40×20]") {
		t.Fatalf("the live area shows:\n%s", shown)
	}

	typing(u, "ab")
	u.press(key{r: 0x7f})

	if len(u.pasted) != 2 || u.input.String() != "a" {
		t.Fatalf("Backspace on a line took %d image(s) and left %q", 2-len(u.pasted), u.input.String())
	}

	u.press(key{r: 0x7f})
	u.press(key{r: 0x7f})

	if len(u.pasted) != 1 || !u.input.empty() {
		t.Fatalf("Backspace on an empty line left %d image(s)", len(u.pasted))
	}

	u.press(key{r: 0x03})

	if len(u.pasted) != 0 {
		t.Fatal("Ctrl-C left the images")
	}
}

// What the clipboard holds that is no image is said, and nothing is put in
// the input.
func TestAClipboardWithoutAnImageIsSaid(t *testing.T) {
	for _, held := range []struct {
		data []byte
		err  error
		said string
	}{
		{nil, errNoImage, "holds no image"},
		{[]byte("just text"), nil, "not a PNG, JPEG, GIF, or WebP"},
		{nil, errors.New("no access"), "no access"},
	} {
		u := quiet()
		u.clipboard = holding(held.data, held.err)

		u.press(key{r: 0x16})
		u.wait.Wait()

		if len(u.pasted) != 0 || !strings.Contains(u.notice, held.said) {
			t.Errorf("pasted %d, notice %q; want it to say %q", len(u.pasted), u.notice, held.said)
		}
	}
}

// What the clipboard program lists is looked through for an image a model
// takes, and that one is read.
func TestTheClipboardIsReadForAnImage(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "image.png")
	os.WriteFile(file, pngOf(t, 4, 2), 0o644)
	program := "#!/bin/sh\ncase \"$1\" in\n--list-types) printf 'text/plain\\nimage/bmp\\nimage/png\\n';;\n*) [ \"$3\" = image/png ] && cat " + file + ";;\nesac\n"
	os.WriteFile(filepath.Join(dir, "wl-paste"), []byte(program), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	list := []string{"--list-types"}
	get := func(kind string) []string { return []string{"--no-newline", "--type", kind} }
	data, err := listedClipboard(context.Background(), "wl-paste", "wl-clipboard", list, get)

	if err != nil || !bytes.Equal(data, pngOf(t, 4, 2)) {
		t.Fatalf("read %d bytes, %v", len(data), err)
	}

	os.WriteFile(filepath.Join(dir, "wl-paste"), []byte("#!/bin/sh\necho text/plain\n"), 0o755)

	if _, err := listedClipboard(context.Background(), "wl-paste", "wl-clipboard", list, get); !errors.Is(err, errNoImage) {
		t.Fatalf("a clipboard of text: %v", err)
	}

	if _, err := listedClipboard(context.Background(), "no-such-clipboard", "it", list, get); err == nil || !strings.Contains(err.Error(), "is not installed") {
		t.Fatalf("a program that is not there: %v", err)
	}
}

// Enter sends the images with what is typed, or alone, as a task, and the
// conversation shows them under it; the history shows them where they were
// given.
func TestPastedImagesGoWithTheTask(t *testing.T) {
	u := quiet()
	given := make(chan protocol.RunParams, 2)
	u.conn = attached(t, func(params protocol.RunParams, run *frontend.Run) (frontend.Work, error) {
		given <- params

		return func(context.Context) (string, error) { return "s1", nil }, nil
	})
	u.clipboard = holding(pngOf(t, 40, 20), nil)

	u.press(key{r: 0x16})
	u.wait.Wait()
	typing(u, "what is this?")
	u.press(key{r: '\r'})
	u.wait.Wait()

	sent := <-given

	if sent.Objective != "what is this?" || len(sent.Images) != 1 || len(u.pasted) != 0 {
		t.Fatalf("sent %q with %d image(s); %d left in the input", sent.Objective, len(sent.Images), len(u.pasted))
	}

	if shown := drawn(u); !inOrder(shown, "what is this?", "[image 1: png, 40×20]") {
		t.Fatalf("the conversation shows:\n%s", shown)
	}

	u.press(key{r: 0x16})
	u.wait.Wait()
	u.press(key{r: '\r'})
	u.wait.Wait()

	if alone := <-given; alone.Objective != "" || len(alone.Images) != 1 {
		t.Fatalf("an image alone was sent as %q with %d image(s)", alone.Objective, len(alone.Images))
	}

	u.tell(&job{}, protocol.Event{Type: protocol.EventObjective, Text: "and this?", Images: []protocol.Image{{MediaType: "image/png", Data: pngOf(t, 8, 6)}}})

	if shown := drawn(u); !inOrder(shown, "and this?", "[image 1: png, 8×6]") {
		t.Fatalf("the history shows:\n%s", shown)
	}
}

// attached returns a front end's connection to a host whose runs are
// handled by run.
func attached(t *testing.T, run func(protocol.RunParams, *frontend.Run) (frontend.Work, error)) *protocol.FrontendConn {
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

	host, err := frontend.Connect(hostReads, hostWrites, protocol.FrontendInitializeParams{Model: "m"})

	if err != nil {
		t.Fatal(err)
	}

	go host.Serve(frontend.Handlers{Run: run})
	t.Cleanup(func() { frontendWrites.Close() })

	return <-dialed
}

// An export names the images a person gave, under their words.
func TestAnExportNamesTheImagesGiven(t *testing.T) {
	told := protocol.HistoryResult{Session: "s", Events: []protocol.Event{
		{Type: protocol.EventObjective, Text: "what is this?", Images: []protocol.Image{{MediaType: "image/png", Data: pngOf(t, 40, 20)}}},
	}}

	if document := markdownOf(told, time.Now()); !strings.Contains(document, "**You**\n\nwhat is this?\n\n*[image 1: png, 40×20]*\n") {
		t.Fatalf("the export:\n%s", document)
	}
}
