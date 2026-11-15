package tui

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/LiasCode/rumbo/protocol"
)

// Completion works from the same descriptions the commands do. A command's
// name is completed from the functions the host and the provider described,
// and an argument from what its parameter says it accepts: fixed values, or
// the function that lists them, so /open completes sessions and /use
// completes providers without this file knowing either.

// maxSuggested bounds how many candidates the status bar lists.
const maxSuggested = 12

// complete returns the input line after a Tab, and a hint to show: the
// candidates when there are several, or why there are none.
func (u *ui) complete(line string) (completed, hint string) {
	head, token := splitLast(line)

	candidates, hint := u.candidates(head)

	if hint != "" {
		return line, hint
	}

	matches := narrow(candidates, token)

	switch len(matches) {
	case 0:
		return line, "no completion for " + token

	case 1:
		return head + matches[0] + " ", ""
	}

	return head + commonPrefix(matches), suggest(matches)
}

// candidates returns what could come after head, which is everything up to
// the word being typed.
func (u *ui) candidates(head string) (candidates []string, hint string) {
	words := strings.Fields(head)

	if len(words) == 0 {
		u.mu.Lock()
		defer u.mu.Unlock()

		return u.commandNames(), ""
	}

	position := len(words) - 1
	name := strings.TrimPrefix(words[0], "/")

	if index := slices.IndexFunc(u.info.Functions, func(function protocol.Function) bool { return function.Name == name }); index >= 0 && words[0] != commandSet {
		param, found := parameter(u.info.Functions[index], position)

		if !found {
			return nil, ""
		}

		return u.accepts(param, protocol.TargetHost)
	}

	if words[0] == commandHelp || words[0] == commandOptions {
		return nil, ""
	}

	// Everything else is about the provider, which has to be asked.
	described, err := u.describe()

	if err != nil {
		return nil, err.Error()
	}

	if words[0] == commandSet {
		return u.settable(described, words[1:])
	}

	if index := slices.IndexFunc(described.Offers.Functions, func(function protocol.Function) bool { return function.Name == name }); index >= 0 {
		if param, found := parameter(described.Offers.Functions[index], position); found {
			return u.accepts(param, protocol.TargetProvider)
		}
	}

	return nil, ""
}

// settable completes /set: first what can be set, then the values it takes.
func (u *ui) settable(described protocol.DescribeResult, chosen []string) (candidates []string, hint string) {
	if len(chosen) == 0 {
		names := []string{"model"}

		for _, option := range described.Offers.Options {
			names = append(names, option.Name)
		}

		return names, ""
	}

	if len(chosen) > 1 {
		return nil, ""
	}

	if chosen[0] == "model" {
		if described.Offers.Model == nil {
			return nil, ""
		}

		return u.accepts(*described.Offers.Model, protocol.TargetProvider)
	}

	for _, option := range described.Offers.Options {
		if option.Name == chosen[0] {
			return u.accepts(option, protocol.TargetProvider)
		}
	}

	return nil, ""
}

// accepts returns the values an option takes, asking its function for them
// when it names one. target says whose function that is. When the function
// cannot answer, the hint says why.
func (u *ui) accepts(option protocol.Option, target string) (values []string, hint string) {
	if option.Type == protocol.OptionBoolean {
		return []string{"true", "false"}, ""
	}

	if option.Values == "" {
		return option.Enum, ""
	}

	var listed []protocol.Value

	if target == protocol.TargetHost {
		result, err := u.conn.InvokeHost(option.Values, nil)

		if err != nil {
			return nil, err.Error()
		}

		listed = result.Values
	} else {
		raw, err := u.conn.Invoke(option.Values, nil)

		if err != nil {
			return nil, err.Error()
		}

		var result protocol.ValuesResult
		_ = json.Unmarshal(raw, &result)
		listed = result.Values
	}

	values = make([]string, len(listed))

	for index, value := range listed {
		values[index] = value.Value
	}

	return values, ""
}

// commandNames returns every command known without asking anyone: the
// front end's own, the host's, and the provider's once it has been
// described. The caller holds the lock.
func (u *ui) commandNames() []string {
	names := []string{commandHelp, commandOptions, commandSet}

	for _, function := range u.info.Functions {
		names = append(names, "/"+function.Name)
	}

	if u.described != nil {
		for _, function := range u.described.Offers.Functions {
			if name := "/" + function.Name; !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}

	slices.Sort(names)

	return names
}

func parameter(function protocol.Function, position int) (protocol.Option, bool) {
	if position < 0 || position >= len(function.Params) {
		return protocol.Option{}, false
	}

	return function.Params[position], true
}

// splitLast separates the word being typed from what comes before it.
func splitLast(line string) (head, token string) {
	index := strings.LastIndexByte(line, ' ')

	return line[:index+1], line[index+1:]
}

func narrow(candidates []string, token string) []string {
	var matches []string

	for _, candidate := range candidates {
		if strings.HasPrefix(candidate, token) {
			matches = append(matches, candidate)
		}
	}

	return matches
}

// commonPrefix returns what every candidate starts with.
func commonPrefix(candidates []string) string {
	prefix := candidates[0]

	for _, candidate := range candidates[1:] {
		for !strings.HasPrefix(candidate, prefix) {
			prefix = prefix[:len(prefix)-1]
		}
	}

	return prefix
}

// suggest lists candidates for the status bar.
func suggest(candidates []string) string {
	more := ""

	if len(candidates) > maxSuggested {
		more = " ..."
		candidates = candidates[:maxSuggested]
	}

	return strings.Join(candidates, "  ") + more
}
