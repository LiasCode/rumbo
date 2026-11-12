// Package project answers what the project is: where it starts, which files
// belong to it, and whether they changed. The tree shown to the model and
// the decision to verify the work both depend on the same file list.
package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rumbo/internal/tool"
)

// maxWalkedFiles bounds the list outside a git repository, where nothing
// says which files are noise.
const maxWalkedFiles = 20_000

// skippedDirs are never walked outside a git repository, where there is no
// ignore file to consult.
var skippedDirs = map[string]bool{
	"node_modules": true, "vendor": true, "target": true, "dist": true, "build": true, "__pycache__": true,
}

// Root returns the directory that identifies the project dir is part of: the
// top of its git repository, or dir itself when it is not in one. Settings
// and sessions belong to the root, so they are found from any subdirectory.
func Root(ctx context.Context, dir string) string {
	if root := strings.TrimSpace(git(ctx, dir, "git rev-parse --show-toplevel 2>/dev/null")); root != "" {
		return root
	}

	return dir
}

// Files lists the files under dir, relative to it. In a git repository they
// are the tracked files and the untracked ones that are not ignored;
// elsewhere, everything outside hidden and dependency directories.
func Files(ctx context.Context, dir string) []string {
	if strings.TrimSpace(git(ctx, dir, "git rev-parse --is-inside-work-tree 2>/dev/null")) != "true" {
		return walk(dir)
	}

	output := strings.TrimSpace(git(ctx, dir, "git ls-files --cached --others --exclude-standard"))

	if output == "" {
		return nil
	}

	return strings.Split(output, "\n")
}

// Snapshot fingerprints the files under dir by path, size, and modification
// time. Two equal snapshots mean nothing in the project was touched in
// between, whatever commands ran.
func Snapshot(ctx context.Context, dir string) string {
	hash := sha256.New()

	for _, file := range Files(ctx, dir) {
		info, err := os.Lstat(filepath.Join(dir, file))

		if err != nil {
			fmt.Fprintf(hash, "%s\tmissing\n", file)
			continue
		}

		fmt.Fprintf(hash, "%s\t%d\t%d\n", file, info.Size(), info.ModTime().UnixNano())
	}

	return hex.EncodeToString(hash.Sum(nil))
}

func walk(dir string) []string {
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

		if len(files) >= maxWalkedFiles {
			return filepath.SkipAll
		}

		relative, _ := filepath.Rel(dir, path)
		files = append(files, filepath.ToSlash(relative))

		return nil
	})

	return files
}

func git(ctx context.Context, dir, command string) string {
	output, _ := tool.Shell(ctx, dir, command, 10*time.Second)

	return output
}
