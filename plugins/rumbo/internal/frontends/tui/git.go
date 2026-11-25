package tui

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// The changes panel shows what changed in the project's folder, as Git sees
// it: the patch of every tracked file against the last commit, and of every
// new file Git does not ignore. rumbo keeps the conversation, not the files,
// so Git is how a person sees what a run did to them, as in rumbo-gui. It
// only reads: Git runs without taking its optional locks.

// mostNewFiles is the most new files given a patch; the rest are only
// counted, since a run can make many, such as a build's output.
const mostNewFiles = 50

// changes are what a folder holds now against its last commit.
type changes struct {
	// branch is the branch checked out, empty on a detached head.
	branch string
	// patch is the patch, in Git's format, of every file that changed, and
	// moreNew the new files left out of it.
	patch   string
	moreNew int
}

// readChanges reads what changed in dir. It fails, saying why, when the
// folder is not in a Git repository or Git cannot be run.
func readChanges(dir string) (changes, error) {
	if _, err := git(dir, "rev-parse", "--is-inside-work-tree"); err != nil {
		if strings.Contains(err.Error(), "not a git repository") {
			return changes{}, errors.New("this folder is not in a Git repository, so its changes cannot be shown")
		}

		return changes{}, err
	}

	var read changes

	// Fails on a detached head, which has no branch to name.
	if branch, err := git(dir, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		read.branch = strings.TrimSpace(branch)
	}

	// A repository without a commit has no HEAD to compare with: every file
	// in it is new.
	if _, err := git(dir, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		patch, err := git(dir, "diff", "HEAD", "--no-color", "--no-ext-diff", "--relative")

		if err != nil {
			return changes{}, err
		}

		read.patch = patch
	}

	untracked, err := git(dir, "ls-files", "--others", "--exclude-standard", "-z")

	if err != nil {
		return changes{}, err
	}

	added := slices.DeleteFunc(strings.Split(untracked, "\x00"), func(path string) bool { return path == "" })

	for index, path := range added {
		if index == mostNewFiles {
			read.moreNew = len(added) - mostNewFiles
			break
		}

		// Exits with 1 when the files differ, which they always do here.
		patch, err := runGit(dir, []int{0, 1}, "diff", "--no-color", "--no-index", "--", "/dev/null", path)

		if err != nil {
			return changes{}, err
		}

		read.patch += patch
	}

	return read, nil
}

// branchOf is what is checked out in dir: its branch, or the short hash of
// the commit on a detached head. It is empty outside Git, or before the
// first commit of a detached head.
func branchOf(dir string) string {
	if branch, err := git(dir, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		return strings.TrimSpace(branch)
	}

	commit, err := git(dir, "rev-parse", "--short", "HEAD")

	if err != nil {
		return ""
	}

	return strings.TrimSpace(commit)
}

func git(dir string, args ...string) (string, error) {
	return runGit(dir, []int{0}, args...)
}

// runGit runs Git in dir, taking any of ok as its exit status to succeed.
func runGit(dir string, ok []int, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")

	var stdout, stderr bytes.Buffer

	command.Stdout, command.Stderr = &stdout, &stderr

	err := command.Run()

	var exited *exec.ExitError

	switch {
	case err == nil, errors.As(err, &exited) && slices.Contains(ok, exited.ExitCode()):
		return stdout.String(), nil
	case errors.As(err, &exited):
		said := strings.TrimSpace(stderr.String())

		if said == "" {
			said = "git " + strings.Join(args, " ") + " failed"
		}

		return "", errors.New(said)
	}

	return "", fmt.Errorf("Git could not be run: %w", err)
}

// changedFile is one file of a patch: its path, its lines, and how many it
// adds and takes away.
type changedFile struct {
	path    string
	lines   []string
	added   int
	removed int
}

// filesOf splits a patch into its files, keeping of each the lines that
// show what changed: its hunks.
func filesOf(patch string) []changedFile {
	var files []changedFile

	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			files = append(files, changedFile{path: pathOf(line)})
			continue
		}

		if len(files) == 0 {
			continue
		}

		file := &files[len(files)-1]

		switch {
		case strings.HasPrefix(line, "+++ ") && len(file.lines) == 0:
			if path, found := strings.CutPrefix(line, "+++ b/"); found {
				file.path = path
			}
		case strings.HasPrefix(line, "--- ") && len(file.lines) == 0:
		case strings.HasPrefix(line, "@@"), strings.HasPrefix(line, "Binary files "), len(file.lines) > 0:
			file.lines = append(file.lines, line)

			switch {
			case strings.HasPrefix(line, "+"):
				file.added++
			case strings.HasPrefix(line, "-"):
				file.removed++
			}
		}
	}

	for index := range files {
		file := &files[index]

		for len(file.lines) > 0 && file.lines[len(file.lines)-1] == "" {
			file.lines = file.lines[:len(file.lines)-1]
		}
	}

	return files
}

// pathOf reads the path a patch's file header names, as it is after the
// change.
func pathOf(header string) string {
	names := strings.TrimPrefix(header, "diff --git ")

	if index := strings.LastIndex(names, " b/"); index >= 0 {
		return names[index+3:]
	}

	return names
}
