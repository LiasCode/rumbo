package cli

import (
	"fmt"
	"runtime/debug"
	"strings"
	"unicode"

	"github.com/LiasCode/rumbo/protocol"
)

// version is the release this binary was built as. The release build sets
// it with -ldflags "-X <module>/internal/cli.version=<version>"; a build
// from source leaves it empty and says which commit it came from.
var version = ""

// commands are the words rumbo reads as a command rather than an
// objective.
var commands = []string{"help", "version", "provide", "attach", "extend", "serve", "provider", "frontend", "extension", "system", "config", "sessions", "skills", "log", "request"}

// describeVersion says what this binary is, and the protocol version it
// speaks.
func describeVersion() string {
	return fmt.Sprintf("rumbo %s (protocol %d)", release(), protocol.Version)
}

// release names this build, which every response records with it.
func release() string {
	info, _ := debug.ReadBuildInfo()

	return built(version, info)
}

// built names this build: the release it was built as, else the module
// version `go install` recorded, else the commit a build from source was
// made from.
func built(release string, info *debug.BuildInfo) string {
	if release != "" {
		return release
	}

	if info == nil {
		return "development build"
	}

	if module := info.Main.Version; module != "" && module != "(devel)" {
		return strings.TrimPrefix(module, "v")
	}

	described := "development build"
	revision, modified := "", false

	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}

	if revision == "" {
		return described
	}

	described += " of " + revision[:min(len(revision), 12)]

	if modified {
		described += " with local changes"
	}

	return described
}

// mistyped returns the command a lone word was most likely meant to be. Run
// as a task, a mistyped command would reach the model, which acts on it.
func mistyped(word string) (string, bool) {
	if word == "" || strings.HasPrefix(word, "-") || strings.ContainsFunc(word, unicode.IsSpace) {
		return "", false
	}

	word = strings.ToLower(word)

	closest, best := "", -1

	for _, command := range commands {
		// Short commands tolerate one slip, longer ones two.
		allowed := 1

		if len(command) > 4 {
			allowed = 2
		}

		if apart := distance(word, command); apart <= allowed && (best < 0 || apart < best) {
			closest, best = command, apart
		}
	}

	return closest, best >= 0
}

// distance counts the insertions, deletions, substitutions, and swaps of
// neighbouring letters that turn a into b.
func distance(a, b string) int {
	x, y := []rune(a), []rune(b)
	d := make([][]int, len(x)+1)

	for i := range d {
		d[i] = make([]int, len(y)+1)
		d[i][0] = i
	}

	for j := range d[0] {
		d[0][j] = j
	}

	for i := 1; i <= len(x); i++ {
		for j := 1; j <= len(y); j++ {
			cost := 1

			if x[i-1] == y[j-1] {
				cost = 0
			}

			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)

			if i > 1 && j > 1 && x[i-1] == y[j-2] && x[i-2] == y[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}

	return d[len(x)][len(y)]
}
