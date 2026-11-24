package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // registers the format with image.DecodeConfig
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/LiasCode/rumbo/internal/picture"
	"github.com/LiasCode/rumbo/protocol"
)

// A terminal pastes text only, so Ctrl-V reads an image from the desktop's
// clipboard itself, and the input keeps it for the next task or steer.

// clipboardTimeout bounds how long reading the clipboard may take.
const clipboardTimeout = 5 * time.Second

// clipboardTypes are the image types a model takes, as a clipboard names
// them, the most likely first.
var clipboardTypes = []string{"image/png", "image/jpeg", "image/webp", "image/gif"}

// errNoImage says the clipboard holds no image a model takes.
var errNoImage = errors.New("the clipboard holds no image; your terminal pastes text, often with Ctrl-Shift-V")

// prepared returns the image in data as the model will be shown it, which
// is also what is sent: no larger than it needs to be.
func prepared(data []byte) (protocol.Image, error) {
	kind := picture.Kind(data)

	if kind == "" {
		return protocol.Image{}, errors.New("the clipboard's image is not a PNG, JPEG, GIF, or WebP image")
	}

	shown, _, err := picture.Prepare(data, kind)

	return shown, err
}

// imageLabel says what an image is, such as "png, 1280×720".
func imageLabel(given protocol.Image) string {
	format := strings.TrimPrefix(given.MediaType, "image/")

	if config, _, err := image.DecodeConfig(bytes.NewReader(given.Data)); err == nil {
		return fmt.Sprintf("%s, %d×%d", format, config.Width, config.Height)
	}

	return format
}

// imageLabels shows images as a row of their labels.
func imageLabels(images []protocol.Image) string {
	labels := make([]string, len(images))

	for index, given := range images {
		labels[index] = fmt.Sprintf("[image %d: %s]", index+1, imageLabel(given))
	}

	return strings.Join(labels, "  ")
}

// readClipboard returns the image in the clipboard of the desktop rumbo runs
// in, through what that desktop offers.
func readClipboard(ctx context.Context) ([]byte, error) {
	switch {
	case runtime.GOOS == "darwin":
		return macClipboard(ctx)

	case os.Getenv("WAYLAND_DISPLAY") != "":
		return listedClipboard(ctx, "wl-paste", "wl-clipboard",
			[]string{"--list-types"},
			func(kind string) []string { return []string{"--no-newline", "--type", kind} })

	case os.Getenv("DISPLAY") != "":
		return listedClipboard(ctx, "xclip", "xclip",
			[]string{"-selection", "clipboard", "-target", "TARGETS", "-out"},
			func(kind string) []string { return []string{"-selection", "clipboard", "-target", kind, "-out"} })
	}

	return nil, errors.New("there is no desktop clipboard where rumbo runs, as over ssh; give the image's path in the task, and the model reads it")
}

// listedClipboard reads a clipboard whose program lists the types it holds,
// and then gives the one asked for.
func listedClipboard(ctx context.Context, program, packaged string, list []string, get func(kind string) []string) ([]byte, error) {
	if _, err := exec.LookPath(program); err != nil {
		return nil, fmt.Errorf("%s is not installed; install %s to paste images", program, packaged)
	}

	listed, err := exec.CommandContext(ctx, program, list...).Output()

	if err != nil {
		return nil, errNoImage
	}

	held := strings.Fields(string(listed))

	for _, kind := range clipboardTypes {
		if !slices.Contains(held, kind) {
			continue
		}

		data, err := exec.CommandContext(ctx, program, get(kind)...).Output()

		if err != nil {
			return nil, fmt.Errorf("reading the clipboard with %s: %w", program, err)
		}

		return data, nil
	}

	return nil, errNoImage
}

// macPasteboard prints the pasteboard's image as base64 PNG, from a PNG or
// the TIFF that screenshots put there, for osascript to run as JavaScript.
const macPasteboard = `ObjC.import('AppKit');
function run() {
	var board = $.NSPasteboard.generalPasteboard;
	var data = board.dataForType($.NSPasteboardTypePNG);
	if (data.isNil()) {
		var tiff = board.dataForType($.NSPasteboardTypeTIFF);
		if (tiff.isNil()) { return ''; }
		data = $.NSBitmapImageRep.imageRepWithData(tiff).representationUsingTypeProperties($.NSBitmapImageFileTypePNG, $());
	}
	return data.base64EncodedStringWithOptions(0).js;
}`

// macClipboard reads the image in the macOS pasteboard.
func macClipboard(ctx context.Context) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "osascript", "-l", "JavaScript", "-e", macPasteboard).Output()

	if err != nil {
		return nil, fmt.Errorf("reading the clipboard with osascript: %w", err)
	}

	encoded := strings.TrimSpace(string(out))

	if encoded == "" {
		return nil, errNoImage
	}

	return base64.StdEncoding.DecodeString(encoded)
}
