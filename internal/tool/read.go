package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/LiasCode/rumbo/internal/picture"
	"github.com/LiasCode/rumbo/protocol"
)

// Read returns the contents of a text file, in windows when it is large, or
// shows the model an image.
type Read struct {
	seen *seen
}

// Def implements Tool.
func (Read) Def() protocol.ToolDef {
	return protocol.ToolDef{
		Name:        "read",
		Description: "Read a text file, or look at an image (PNG, JPEG, GIF, WebP). Returns 500 lines per call unless limit says otherwise; pass offset (1-based line) to read further. For a large file, search with bash first and read only the part you need.",
		Schema: schema(`{
			"path": {"type": "string", "description": "File path, relative to the working directory or absolute"},
			"offset": {"type": "integer", "description": "First line to read, starting at 1"},
			"limit": {"type": "integer", "description": "Maximum number of lines to read"}
		}`, "path"),
	}
}

// Run implements Tool.
func (r Read) Run(_ context.Context, dir string, args json.RawMessage) (Result, error) {
	var input struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}

	if err := decode(args, &input); err != nil {
		return Result{}, err
	}

	path, err := resolve(dir, input.Path)

	if err != nil {
		return Result{}, err
	}

	raw, err := os.ReadFile(path)

	if err != nil {
		return Result{}, err
	}

	if kind := picture.Kind(raw); kind != "" {
		shown, about, err := picture.Prepare(raw, kind)

		if err != nil {
			return Result{}, fmt.Errorf("%s: %w", input.Path, err)
		}

		r.seen.record(path)

		return Result{Text: fmt.Sprintf("[image %s: %s]", input.Path, about), Images: []protocol.Image{shown}}, nil
	}

	if strings.ContainsRune(string(raw[:min(len(raw), 8000)]), 0) {
		return Result{}, fmt.Errorf("%s is a binary file; inspect it with bash instead", input.Path)
	}

	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	total := len(lines)
	start := max(input.Offset, 1) - 1

	if start >= total {
		return Result{}, fmt.Errorf("offset %d is past the end of the file (%d lines)", input.Offset, total)
	}

	limit := readLines

	if input.Limit > 0 {
		limit = min(input.Limit, maxReadLines)
	}

	end := start
	size := 0

	for end < total && end-start < limit && size+len(lines[end])+1 <= maxReadBytes {
		size += len(lines[end]) + 1
		end++
	}

	if end == start {
		return Result{}, fmt.Errorf("line %d is longer than %d bytes; inspect it with bash instead", start+1, maxReadBytes)
	}

	r.seen.record(path)

	text := strings.Join(lines[start:end], "\n")

	if end < total {
		text += fmt.Sprintf("\n[lines %d-%d of %d; continue with offset %d]", start+1, end, total, end+1)
	}

	return Result{Text: text}, nil
}
