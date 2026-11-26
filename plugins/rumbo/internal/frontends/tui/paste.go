package tui

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/LiasCode/rumbo/internal/home"
	"github.com/LiasCode/rumbo/internal/picture"
)

// A terminal pastes text only, so Ctrl-V reads the desktop's clipboard
// itself, for the files copied in a file manager or for an image. The model
// is given files, by their paths, so the image is saved as one, and the
// input keeps the paths for the next task or steer. A file dropped on the
// terminal is pasted as its path, which is taken for the file too.

// clipboardTimeout bounds how long reading the clipboard may take.
const clipboardTimeout = 5 * time.Second

// clipboardTypes are the image types a model takes, as a clipboard names
// them, the most likely first.
var clipboardTypes = []string{"image/png", "image/jpeg", "image/webp", "image/gif"}

// uriList is how a clipboard names the files copied in a file manager.
const uriList = "text/uri-list"

// held is what a clipboard holds that can be given: files, or an image.
type held struct {
	files []string
	image []byte
}

// errNothingHeld says the clipboard holds no file and no image a model
// takes.
var errNothingHeld = errors.New("the clipboard holds no file and no image; your terminal pastes text, often with Ctrl-Shift-V")

// keep saves the clipboard's image in data as a file and returns its path.
// It goes in the user's cache, so a session shown again later still finds
// it, under a name made of what it holds, so the same image is kept once.
func keep(data []byte) (string, error) {
	kind := picture.Kind(data)

	if kind == "" {
		return "", errors.New("the clipboard's image is not a PNG, JPEG, GIF, or WebP image")
	}

	rumbo, err := home.Dir()

	if err != nil {
		return "", fmt.Errorf("the pasted image has nowhere to be kept: %w", err)
	}

	dir := filepath.Join(rumbo, "pasted")

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	sum := sha256.Sum256(data)
	path := filepath.Join(dir, fmt.Sprintf("pasted-%x.%s", sum[:4], strings.TrimPrefix(kind, "image/")))

	return path, os.WriteFile(path, data, 0o600)
}

// located is the path a word names: its own, or that of a file:// address,
// as a clipboard and some terminals give one.
func located(word string) string {
	if address, err := url.Parse(word); err == nil && address.Scheme == "file" {
		return fileAddressPath(address)
	}

	return word
}

// listed are the paths of the files a clipboard's list of addresses names.
func listed(uris string) []string {
	var files []string

	for _, line := range strings.Fields(uris) {
		if path := located(line); path != line {
			files = append(files, path)
		}
	}

	return files
}

// exists reports whether path is the whole path of something there is.
func exists(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}

	_, err := os.Stat(path)

	return err == nil
}

// dropped returns the files a paste names, when all of it is the whole
// paths of files there are, which is what a terminal pastes of those
// dropped on it: a path to a line, or several in one, quoted as a shell
// takes them. Any other paste is text, and it returns none.
func dropped(text string) []string {
	var files []string

	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		line = strings.TrimSpace(line)

		// A terminal that quotes nothing pastes a path with its spaces.
		if path := located(line); exists(path) {
			files = append(files, path)
			continue
		}

		words := unquoted(line)

		if len(words) == 0 {
			return nil
		}

		for _, word := range words {
			path := located(word)

			if !exists(path) {
				return nil
			}

			files = append(files, path)
		}
	}

	return files
}

// unquoted splits a line in words as a shell does: at its spaces, but for
// those in quotes or, where a backslash escapes, after one.
func unquoted(line string) []string {
	var words []string
	var word strings.Builder

	quote, escaped, open := rune(0), false, false

	for _, r := range line {
		switch {
		case escaped:
			word.WriteRune(r)
			escaped = false
		case backslashEscapes && r == '\\' && quote != '\'':
			escaped, open = true, true
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '\'' || r == '"'):
			quote, open = r, true
		case quote == 0 && (r == ' ' || r == '\t'):
			if open {
				words = append(words, word.String())
				word.Reset()
			}

			open = false
		default:
			word.WriteRune(r)
			open = true
		}
	}

	if open {
		words = append(words, word.String())
	}

	return words
}

// icons are the pictures that say what kind of file one is, by the end of
// its name. Each is two columns wide in every terminal.
var icons = map[string]string{
	".png": "📷", ".jpg": "📷", ".jpeg": "📷", ".gif": "📷", ".webp": "📷", ".svg": "📷", ".bmp": "📷", ".heic": "📷",
	".pdf": "📕",
	".md":  "📝", ".txt": "📝", ".doc": "📝", ".docx": "📝", ".odt": "📝", ".rtf": "📝",
	".csv": "📊", ".tsv": "📊", ".xls": "📊", ".xlsx": "📊", ".ods": "📊",
	".zip": "📦", ".tar": "📦", ".gz": "📦", ".tgz": "📦", ".xz": "📦", ".bz2": "📦", ".7z": "📦", ".rar": "📦",
	".mp3": "🎵", ".wav": "🎵", ".flac": "🎵", ".ogg": "🎵", ".m4a": "🎵",
	".mp4": "🎬", ".mkv": "🎬", ".mov": "🎬", ".webm": "🎬", ".avi": "🎬",
	".go": "📜", ".py": "📜", ".js": "📜", ".ts": "📜", ".tsx": "📜", ".jsx": "📜", ".rs": "📜", ".c": "📜", ".h": "📜", ".cpp": "📜", ".java": "📜", ".rb": "📜", ".sh": "📜", ".sql": "📜", ".html": "📜", ".css": "📜",
	".json": "📜", ".yaml": "📜", ".yml": "📜", ".toml": "📜", ".xml": "📜",
}

// icon is the picture for a file, or the one of any file when the end of
// its name says nothing of its kind.
func icon(file string) string {
	if known, found := icons[strings.ToLower(filepath.Ext(file))]; found {
		return known
	}

	return "📄"
}

// fileLabels shows files as a row of their names, each after its icon.
func fileLabels(files []string) string {
	labels := make([]string, len(files))

	for index, file := range files {
		labels[index] = icon(file) + " " + filepath.Base(file)
	}

	return strings.Join(labels, "  ")
}

// readClipboard returns the files or the image in the clipboard of the
// desktop rumbo runs in, through what that desktop offers.
func readClipboard(ctx context.Context) (held, error) {
	switch {
	case runtime.GOOS == "darwin":
		return macClipboard(ctx)

	case runtime.GOOS == "windows":
		return held{}, errors.New("rumbo does not read the clipboard on Windows yet; give the file with /attach")

	case os.Getenv("WAYLAND_DISPLAY") != "":
		return listedClipboard(ctx, "wl-paste", "wl-clipboard",
			[]string{"--list-types"},
			func(kind string) []string { return []string{"--no-newline", "--type", kind} })

	case os.Getenv("DISPLAY") != "":
		return listedClipboard(ctx, "xclip", "xclip",
			[]string{"-selection", "clipboard", "-target", "TARGETS", "-out"},
			func(kind string) []string { return []string{"-selection", "clipboard", "-target", kind, "-out"} })
	}

	return held{}, errors.New("there is no desktop clipboard where rumbo runs, as over ssh; give the file with /attach")
}

// listedClipboard reads a clipboard whose program lists the types it holds,
// and then gives the one asked for: the files copied, which are given as
// they are, before an image, which is what one of them may also be there as.
func listedClipboard(ctx context.Context, program, packaged string, list []string, get func(kind string) []string) (held, error) {
	if _, err := exec.LookPath(program); err != nil {
		return held{}, fmt.Errorf("%s is not installed; install %s to paste files and images", program, packaged)
	}

	types, err := exec.CommandContext(ctx, program, list...).Output()

	if err != nil {
		return held{}, errNothingHeld
	}

	offered := strings.Fields(string(types))

	for _, kind := range append([]string{uriList}, clipboardTypes...) {
		if !slices.Contains(offered, kind) {
			continue
		}

		data, err := exec.CommandContext(ctx, program, get(kind)...).Output()

		if err != nil {
			return held{}, fmt.Errorf("reading the clipboard with %s: %w", program, err)
		}

		if kind != uriList {
			return held{image: data}, nil
		}

		if files := listed(string(data)); len(files) > 0 {
			return held{files: files}, nil
		}
	}

	return held{}, errNothingHeld
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
func macClipboard(ctx context.Context) (held, error) {
	out, err := exec.CommandContext(ctx, "osascript", "-l", "JavaScript", "-e", macPasteboard).Output()

	if err != nil {
		return held{}, fmt.Errorf("reading the clipboard with osascript: %w", err)
	}

	encoded := strings.TrimSpace(string(out))

	if encoded == "" {
		return held{}, errNothingHeld
	}

	image, err := base64.StdEncoding.DecodeString(encoded)

	return held{image: image}, err
}
