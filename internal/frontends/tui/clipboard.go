package tui

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// copyAnswer puts the model's last answer, or only its last code block, in
// the clipboard, with OSC 52: the terminal does it, so it works over ssh as
// well, in the terminals that take it.
func (u *ui) copyAnswer(args []string) (answer, error) {
	u.mu.Lock()
	text := u.reply
	u.mu.Unlock()

	if strings.TrimSpace(text) == "" {
		return answer{}, errors.New("there is no answer to copy yet")
	}

	switch strings.Join(args, " ") {
	case "":
	case "code":
		code, found := lastCode(text)

		if !found {
			return answer{}, errors.New("the last answer has no code block; /copy takes all of it")
		}

		text = code
	default:
		return answer{}, errors.New("usage: /copy [code]")
	}

	text = strings.TrimRight(text, "\n")

	u.mu.Lock()
	fmt.Fprint(u.screen.out, escape+"]52;c;"+base64.StdEncoding.EncodeToString([]byte(text))+"\a")
	u.mu.Unlock()

	lines := strings.Count(text, "\n") + 1

	return answer{text: fmt.Sprintf("sent %d line(s) to the clipboard, in a terminal that takes OSC 52; in tmux, with set-clipboard on", lines)}, nil
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
