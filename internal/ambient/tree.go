package ambient

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"rumbo/internal/project"
)

// Limits of the tree section. The tree is expanded level by level until it
// would pass maxTreeLines; deeper directories are shown as a file count.
const (
	maxTreeLines = 150
	maxTreeDepth = 8
	// Files from this size on are shown with their length, so the model
	// searches them instead of reading them whole.
	largeFileBytes = 16_000
	// Larger than this, a file is not read to count its lines.
	countLinesUpTo = 8_000_000
)

// tree renders the project's files as an indented outline.
func tree(ctx context.Context, dir string) string {
	files := project.Files(ctx, dir)

	if len(files) == 0 {
		return ""
	}

	root := newNode()

	for _, file := range files {
		root.insert(strings.Split(file, "/"))
	}

	// The tree is rendered once per depth tried, and sizing a file means
	// reading it, so each file is sized once.
	sizes := map[string]string{}

	describe := func(path string) string {
		if _, known := sizes[path]; !known {
			sizes[path] = size(filepath.Join(dir, path))
		}

		return sizes[path]
	}

	lines := root.render(describe, "", 1, 0)

	for depth := 2; depth <= maxTreeDepth; depth++ {
		deeper := root.render(describe, "", depth, 0)

		if len(deeper) > maxTreeLines {
			break
		}

		lines = deeper
	}

	if len(lines) > maxTreeLines {
		hidden := len(lines) - maxTreeLines
		lines = append(lines[:maxTreeLines], fmt.Sprintf("... (%d more entries)", hidden))
	}

	return strings.Join(lines, "\n")
}

// node is a directory in the tree: its subdirectories and its own files.
type node struct {
	dirs  map[string]*node
	files []string
	count int
}

func newNode() *node {
	return &node{dirs: map[string]*node{}}
}

func (n *node) insert(parts []string) {
	n.count++

	if len(parts) == 1 {
		n.files = append(n.files, parts[0])
		return
	}

	child := n.dirs[parts[0]]

	if child == nil {
		child = newNode()
		n.dirs[parts[0]] = child
	}

	child.insert(parts[1:])
}

// render lists the node's content, which lives at path. Directories at the
// depth limit are collapsed to their file count, and describe adds what is
// worth knowing about a file.
func (n *node) render(describe func(path string) string, path string, depth, level int) []string {
	indent := strings.Repeat("  ", level)
	names := make([]string, 0, len(n.dirs))

	for name := range n.dirs {
		names = append(names, name)
	}

	sort.Strings(names)
	sort.Strings(n.files)

	var lines []string

	for _, name := range names {
		child := n.dirs[name]

		if level+1 >= depth {
			lines = append(lines, fmt.Sprintf("%s%s/ (%d files)", indent, name, child.count))
			continue
		}

		lines = append(lines, indent+name+"/")
		lines = append(lines, child.render(describe, filepath.Join(path, name), depth, level+1)...)
	}

	for _, file := range n.files {
		lines = append(lines, indent+file+describe(filepath.Join(path, file)))
	}

	return lines
}

// size describes a large file, and is empty for an ordinary one. The figure
// is rounded so that an edit to the file rarely changes the tree, which
// would send the whole section to the model again.
func size(path string) string {
	info, err := os.Stat(path)

	if err != nil || info.Size() < largeFileBytes {
		return ""
	}

	if info.Size() > countLinesUpTo {
		return fmt.Sprintf(" (%d MB)", info.Size()/1_000_000)
	}

	content, err := os.ReadFile(path)

	if err != nil {
		return ""
	}

	// Without line breaks, or with a NUL byte, it is not text to count.
	lines := bytes.Count(content, []byte{'\n'})

	if lines == 0 || bytes.IndexByte(content[:min(len(content), 8000)], 0) >= 0 {
		return fmt.Sprintf(" (%d KB)", info.Size()/1000)
	}

	if lines < 1000 {
		return fmt.Sprintf(" (~%d lines)", (lines+50)/100*100)
	}

	return fmt.Sprintf(" (~%.1fk lines)", float64(lines)/1000)
}
