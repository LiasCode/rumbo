package tui

import (
	"strings"
)

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
