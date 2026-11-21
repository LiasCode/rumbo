package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/LiasCode/rumbo/protocol"
)

// Write creates or replaces a whole file.
type Write struct {
	seen *seen
}

// Def implements Tool.
func (Write) Def() protocol.ToolDef {
	return protocol.ToolDef{
		Name:        "write",
		Description: "Create a file or replace its whole content. Parent directories are created. An existing file must have been read first. Use edit for changes to an existing file.",
		Schema: schema(`{
			"path": {"type": "string", "description": "File path, relative to the working directory or absolute"},
			"content": {"type": "string", "description": "Full content of the file"}
		}`, "path", "content"),
	}
}

// Run implements Tool.
func (w Write) Run(_ context.Context, dir string, args json.RawMessage) (Result, error) {
	output, err := w.write(dir, args)

	return text(output), err
}

func (w Write) write(dir string, args json.RawMessage) (string, error) {
	var input struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}

	if err := decode(args, &input); err != nil {
		return "", err
	}

	path, err := resolve(dir, input.Path)

	if err != nil {
		return "", err
	}

	// Replacing a file whole discards whatever is in it, so the model has to
	// know what that is. edit needs no such rule: its exact match fails by
	// itself when the file is not what the model thinks.
	if err := w.seen.overwritable(path, input.Path); err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}

	if err := os.WriteFile(path, []byte(input.Content), 0o644); err != nil {
		return "", err
	}

	w.seen.record(path)

	return fmt.Sprintf("wrote %s (%d lines)", input.Path, strings.Count(input.Content, "\n")+1), nil
}

// Edit replaces one exact piece of text in a file.
type Edit struct {
	seen *seen
}

// Def implements Tool.
func (Edit) Def() protocol.ToolDef {
	return protocol.ToolDef{
		Name:        "edit",
		Description: "Replace text in a file. old must match the file exactly, including whitespace, and must occur once unless all is true.",
		Schema: schema(`{
			"path": {"type": "string", "description": "File path, relative to the working directory or absolute"},
			"old": {"type": "string", "description": "Exact text to replace"},
			"new": {"type": "string", "description": "Replacement text"},
			"all": {"type": "boolean", "description": "Replace every occurrence instead of requiring a single one"}
		}`, "path", "old", "new"),
	}
}

// Run implements Tool.
func (e Edit) Run(_ context.Context, dir string, args json.RawMessage) (Result, error) {
	output, err := e.edit(dir, args)

	return text(output), err
}

func (e Edit) edit(dir string, args json.RawMessage) (string, error) {
	var input struct {
		Path string `json:"path"`
		Old  string `json:"old"`
		New  string `json:"new"`
		All  bool   `json:"all"`
	}

	if err := decode(args, &input); err != nil {
		return "", err
	}

	path, err := resolve(dir, input.Path)

	if err != nil {
		return "", err
	}

	if input.Old == "" {
		return "", fmt.Errorf("old is empty; use write to create a file")
	}

	if input.Old == input.New {
		return "", fmt.Errorf("old and new are identical")
	}

	info, err := os.Stat(path)

	if err != nil {
		return "", err
	}

	raw, err := os.ReadFile(path)

	if err != nil {
		return "", err
	}

	content := string(raw)
	count := strings.Count(content, input.Old)

	if count == 0 {
		return "", fmt.Errorf("old was not found in %s; read the file and copy the text exactly", input.Path)
	}

	if count > 1 && !input.All {
		return "", fmt.Errorf("old occurs %d times in %s; add surrounding lines to make it unique, or set all", count, input.Path)
	}

	content = strings.ReplaceAll(content, input.Old, input.New)

	if err := os.WriteFile(path, []byte(content), info.Mode().Perm()); err != nil {
		return "", err
	}

	// The model read the old text and wrote the new, so it knows the file.
	e.seen.record(path)

	return fmt.Sprintf("edited %s (%d replacement(s))", input.Path, count), nil
}
