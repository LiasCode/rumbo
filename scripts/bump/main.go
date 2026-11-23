// Command bump prepares the next release of rumbo from the commits since the
// last one. Their Conventional Commits types give the version: a breaking
// change raises the major number, feat the minor one, and fix and perf the
// patch. It dates the changelog's Unreleased section with that version,
// commits the changelog, and tags the commit. Pushing the tag publishes the
// release (.github/workflows/release.yml).
//
// Run it from the repository root, with nothing left uncommitted:
//
//	go run ./scripts/bump      # commit and tag the release
//	go run ./scripts/bump -n   # only say which version it would be, and why
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"
)

// level is how much a commit raises the version.
type level int

const (
	none level = iota
	patch
	minor
	major
)

func (l level) String() string {
	return [...]string{"none", "patch", "minor", "major"}[l]
}

// header matches a Conventional Commits summary: its type, an optional
// scope, and the ! that marks a breaking change.
var header = regexp.MustCompile(`^([a-z]+)(\([^)]*\))?(!)?: `)

// breakingFooter matches the footer that marks a breaking change in a body.
var breakingFooter = regexp.MustCompile(`(?m)^BREAKING[ -]CHANGE: `)

// levelOf reads how much one commit message raises the version.
func levelOf(message string) level {
	parts := header.FindStringSubmatch(message)
	if parts == nil {
		return none
	}

	if parts[3] == "!" || breakingFooter.MatchString(message) {
		return major
	}

	switch parts[1] {
	case "feat":
		return minor
	case "fix", "perf":
		return patch
	}

	return none
}

// version is a release's number, without the v of its tag.
type version struct {
	major, minor, patch int
}

func parseVersion(tag string) (version, error) {
	var v version

	if _, err := fmt.Sscanf(tag, "v%d.%d.%d", &v.major, &v.minor, &v.patch); err != nil {
		return version{}, fmt.Errorf("tag %s is not vX.Y.Z", tag)
	}

	return v, nil
}

func (v version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

// raise gives the version that follows v for changes of level l. Before 1.0
// a breaking change raises the minor number, since the README says commands
// and settings may change until then; 1.0 itself is chosen by hand.
func (v version) raise(l level) version {
	if l == major && v.major == 0 {
		l = minor
	}

	switch l {
	case major:
		return version{v.major + 1, 0, 0}
	case minor:
		return version{v.major, v.minor + 1, 0}
	case patch:
		return version{v.major, v.minor, v.patch + 1}
	}

	return v
}

// dated puts the heading of release v, dated day, under the changelog's
// Unreleased heading, so what Unreleased held becomes the release's notes
// and Unreleased is left empty for what comes next. It refuses an empty
// Unreleased section, which the release workflow would refuse too.
func dated(changelog string, v version, day string) (string, error) {
	lines := strings.Split(changelog, "\n")

	start := slices.Index(lines, "## Unreleased")
	if start < 0 {
		return "", errors.New(`CHANGELOG.md has no "## Unreleased" heading`)
	}

	empty := true
	for _, line := range lines[start+1:] {
		if strings.HasPrefix(line, "## ") {
			break
		}

		if strings.TrimSpace(line) != "" {
			empty = false
			break
		}
	}

	if empty {
		return "", errors.New("CHANGELOG.md says nothing under Unreleased; write what the release changes for the people who use it first")
	}

	lines = slices.Insert(lines, start+1, "", fmt.Sprintf("## %s (%s)", v, day))

	return strings.Join(lines, "\n"), nil
}

func main() {
	dryRun := flag.Bool("n", false, "only say which version the next release would be, and why")
	flag.Parse()

	if err := run(*dryRun); err != nil {
		fmt.Fprintln(os.Stderr, "bump:", err)
		os.Exit(1)
	}
}

func run(dryRun bool) error {
	last, err := git("describe", "--tags", "--abbrev=0", "--match", "v[0-9]*")
	if err != nil {
		return err
	}

	current, err := parseVersion(last)
	if err != nil {
		return err
	}

	log, err := git("log", "--format=%B%x00", last+"..HEAD")
	if err != nil {
		return err
	}

	// The highest level among the commits decides, and the commits of that
	// level are why.
	highest := none
	var why []string

	for _, message := range strings.Split(log, "\x00") {
		message = strings.TrimSpace(message)
		l := levelOf(message)

		if l > highest {
			highest = l
			why = nil
		}

		if l == highest && l != none {
			summary, _, _ := strings.Cut(message, "\n")
			why = append(why, summary)
		}
	}

	if highest == none {
		return fmt.Errorf("nothing to release: no feat, fix, perf, or breaking change since %s", last)
	}

	next := current.raise(highest)

	changelog, err := os.ReadFile("CHANGELOG.md")
	if err != nil {
		return err
	}

	updated, err := dated(string(changelog), next, time.Now().Format("2006-01-02"))
	if err != nil {
		return err
	}

	if dryRun {
		fmt.Printf("%s -> %s (%s), for:\n", current, next, highest)
		for _, summary := range why {
			fmt.Println("  " + summary)
		}

		return nil
	}

	// The release commit holds the changelog alone, and the tag must name
	// what was checked.
	status, err := git("status", "--porcelain")
	if err != nil {
		return err
	}

	if status != "" {
		return errors.New("the working tree has uncommitted changes; commit or stash them first")
	}

	if err := os.WriteFile("CHANGELOG.md", []byte(updated), 0o644); err != nil {
		return err
	}

	if _, err := git("commit", "-m", "chore: release "+next.String(), "--", "CHANGELOG.md"); err != nil {
		return err
	}

	if _, err := git("tag", "-a", "v"+next.String(), "-m", "rumbo "+next.String()); err != nil {
		return err
	}

	fmt.Printf("Tagged v%s. Push it to publish the release:\n\n  git push --atomic origin main v%s\n", next, next)

	return nil
}

// git runs git with args and returns what it printed, trimmed. What git says
// on failure goes to stderr.
func git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}

	return strings.TrimSpace(string(out)), nil
}
