package tui

import "strings"

// history is what was entered, oldest first, which Up and Down and Ctrl-R
// go through: what the person asked in the project's sessions, which the
// host keeps and recalls, and after it what was entered here, commands
// too, which lasts as long as the front end. Answers to a function's
// questions and to confirmations are never in it.
type history struct {
	entries []string
	// recalled is the entry in the input, len(entries) when none is, and
	// draft is what was being typed before going back.
	recalled int
	draft    string
}

// earlier puts what the host recalled, most recent first, before what was
// entered here.
func (h *history) earlier(asked []string) {
	older := make([]string, 0, len(asked)+len(h.entries))

	for index := len(asked) - 1; index >= 0; index-- {
		older = append(older, asked[index])
	}

	h.entries = append(older, h.entries...)
	h.recalled += len(asked)
}

// add keeps a line that was entered, unless it repeats the one before, and
// leaves the history at its end.
func (h *history) add(line string) {
	if last := len(h.entries) - 1; last < 0 || h.entries[last] != line {
		h.entries = append(h.entries, line)
	}

	h.recalled = len(h.entries)
	h.draft = ""
}

// recall returns another entry for the input, given what it holds now: an
// older one for a step back, a newer one for a step forward, and past the
// newest what was being typed. It reports false when there is none further.
func (h *history) recall(step int, current string) (string, bool) {
	next := h.recalled + step

	if next < 0 || next > len(h.entries) {
		return "", false
	}

	if h.recalled == len(h.entries) {
		h.draft = current
	}

	h.recalled = next

	if next == len(h.entries) {
		return h.draft, true
	}

	return h.entries[next], true
}

// search returns the newest entry before the one at from that holds query,
// ignoring case, and its index, or -1 when none does.
func (h *history) search(query string, from int) (string, int) {
	query = strings.ToLower(query)

	for index := min(from, len(h.entries)) - 1; index >= 0; index-- {
		if strings.Contains(strings.ToLower(h.entries[index]), query) {
			return h.entries[index], index
		}
	}

	return "", -1
}
