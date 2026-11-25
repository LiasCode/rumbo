package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Text is copied through the terminal (OSC 52), which reaches the
// clipboard over ssh too, and also through the desktop rumbo runs on, since
// a terminal or a tmux that does not take OSC 52 from a program drops it
// without a word: with the program the desktop offers, or else tmux's own
// buffer, which tmux sends on to the terminal's clipboard itself.

// errNoDesktop says text could only be sent through the terminal.
var errNoDesktop = errors.New("there is no desktop clipboard here")

// copyToDesktop puts text in the clipboard of the desktop rumbo runs on, or
// in its primary selection, and says where it went.
func copyToDesktop(ctx context.Context, text string, primary bool) (string, error) {
	run := func(program string, args ...string) (bool, error) {
		if _, err := exec.LookPath(program); err != nil {
			return false, nil
		}

		command := exec.CommandContext(ctx, program, args...)
		command.Stdin = strings.NewReader(text)

		if err := command.Run(); err != nil {
			return true, fmt.Errorf("%s: %w", program, err)
		}

		return true, nil
	}

	selection, xsel := "clipboard", "--clipboard"

	if primary {
		selection, xsel = "primary", "--primary"
	}

	var tried []func() (bool, error)

	switch {
	case runtime.GOOS == "darwin" && !primary:
		tried = append(tried, func() (bool, error) { return run("pbcopy") })
	case os.Getenv("WAYLAND_DISPLAY") != "":
		args := []string{}

		if primary {
			args = append(args, "--primary")
		}

		tried = append(tried, func() (bool, error) { return run("wl-copy", args...) })
	case os.Getenv("DISPLAY") != "":
		tried = append(tried,
			func() (bool, error) { return run("xclip", "-selection", selection, "-in") },
			func() (bool, error) { return run("xsel", xsel, "--input") })
	}

	for _, try := range tried {
		if found, err := try(); found {
			return "the clipboard", err
		}
	}

	// tmux has no primary selection to send on.
	if os.Getenv("TMUX") != "" && !primary {
		if found, err := run("tmux", "load-buffer", "-w", "-"); found {
			return "tmux's buffer, which sends it on to the terminal's clipboard", err
		}
	}

	return "", errNoDesktop
}

// lastCode returns what the last code block of a Markdown text holds.
func lastCode(text string) (string, bool) {
	var (
		fence string
		block []string
		last  []string
		found bool
	)

	for _, line := range strings.Split(text, "\n") {
		mark := fenceLine.FindStringSubmatch(line)

		switch {
		case fence == "" && mark != nil:
			fence, block = mark[1], nil
		case fence != "" && mark != nil && strings.HasPrefix(mark[1], fence) && strings.TrimSpace(line) == mark[1]:
			fence, last, found = "", block, true
		case fence != "":
			block = append(block, line)
		}
	}

	// A block the answer did not close is still its last one.
	if fence != "" {
		last, found = block, true
	}

	return strings.Join(last, "\n"), found
}
