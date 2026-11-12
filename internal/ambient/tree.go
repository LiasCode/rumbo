package ambient

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// Limits of the tree section. The tree is expanded level by level until it
// would pass maxTreeLines; deeper directories are shown as a file count.
const (
	maxTreeLines = 150
	maxTreeDepth = 8
	maxTreeFiles = 20_000
)

// skippedDirs are never walked outside a git repository, where there is no
// ignore file to consult.
var skippedDirs = map[string]bool{
	"node_modules": true, "vendor": true, "target": true, "dist": true, "build": true, "__pycache__": true,
}

// tree renders the project's files as an indented outline.
func tree(ctx context.Context, dir string) string {
	files := gitFiles(ctx, dir)

	if files == nil {
		files = walkFiles(dir)
	}

	if len(files) == 0 {
		return ""
	}

	root := newNode()

	for _, file := range files {
		root.insert(strings.Split(file, "/"))
	}

	lines := root.render(1, 0)

	for depth := 2; depth <= maxTreeDepth; depth++ {
		deeper := root.render(depth, 0)

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

// gitFiles lists tracked and untracked files that are not ignored, or nil when
// dir is not inside a git repository.
func gitFiles(ctx context.Context, dir string) []string {
	if strings.TrimSpace(run(ctx, dir, "git rev-parse --is-inside-work-tree 2>/dev/null")) != "true" {
		return nil
	}

	output := run(ctx, dir, "git ls-files --cached --others --exclude-standard")
	files := strings.Split(strings.TrimSpace(output), "\n")

	if len(files) == 1 && files[0] == "" {
		return []string{}
	}

	return files
}

func walkFiles(dir string) []string {
	var files []string

	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return nil
		}

		name := entry.Name()

		if entry.IsDir() {
			if strings.HasPrefix(name, ".") || skippedDirs[name] {
				return filepath.SkipDir
			}

			return nil
		}

		if len(files) >= maxTreeFiles {
			return filepath.SkipAll
		}

		relative, _ := filepath.Rel(dir, path)
		files = append(files, filepath.ToSlash(relative))

		return nil
	})

	return files
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

// render lists the node's content. Directories at the depth limit are
// collapsed to their file count.
func (n *node) render(depth, level int) []string {
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
		lines = append(lines, child.render(depth, level+1)...)
	}

	for _, file := range n.files {
		lines = append(lines, indent+file)
	}

	return lines
}
