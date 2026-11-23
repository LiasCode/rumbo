package ambient

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"
)

// A skill is what the user wrote down about one kind of task, in the Agent
// Skills format (https://agentskills.io): a directory with a SKILL.md, whose
// frontmatter names the skill and says what it is for. Only that description
// is sent with every request; the model reads the rest when a task needs it.

// skillsIntro opens the section, so the model knows what the list is for.
const skillsIntro = "Each skill below holds instructions for one kind of task. When the task, or part of it, is one a skill describes, read its SKILL.md before doing that part, and follow it. Paths in a skill are relative to the directory of its SKILL.md."

// maxDescriptionBytes bounds what one skill adds to every request. It is the
// longest description the Agent Skills format allows.
const maxDescriptionBytes = 1024

// Skill is one skill the model is told of.
type Skill struct {
	Name        string
	Description string
	// Path is its SKILL.md.
	Path string
}

// LeftOut is a skill the model is not told of, and why.
type LeftOut struct {
	// Path is its SKILL.md, or the directory that has none.
	Path   string
	Reason string
}

// Skills are the skills that apply in a directory.
type Skills struct {
	// Places are where skills are looked for, the most specific last.
	Places []string
	// Found are the skills the model is told of, by name.
	Found []Skill
	// Left are those it is not told of, in the order they were met.
	Left []LeftOut
}

// FindSkills returns the skills the user's plugins bring, in the places
// given, the user's own, in skills beside rumbo's settings, and the
// project's, in .agents/skills from the project's root down to dir. A
// skill with the name of another one replaces it in a more specific place.
func FindSkills(ctx context.Context, dir string, plugins []string) Skills {
	found := Skills{Places: slices.Clone(plugins)}

	if user, ok := userDir(); ok {
		found.Places = append(found.Places, filepath.Join(user, "skills"))
	}

	for _, candidate := range projectDirs(ctx, dir) {
		found.Places = append(found.Places, filepath.Join(candidate, ".agents", "skills"))
	}

	named := map[string]Skill{}

	for _, place := range found.Places {
		read, left := readSkills(place)
		found.Left = append(found.Left, left...)

		for _, each := range read {
			if earlier, ok := named[each.Name]; ok {
				found.Left = append(found.Left, LeftOut{Path: earlier.Path, Reason: "replaced by " + each.Path + ", a skill of the same name"})
			}

			named[each.Name] = each
		}
	}

	// By name, so the section reads the same until a skill changes.
	for _, name := range slices.Sorted(maps.Keys(named)) {
		found.Found = append(found.Found, named[name])
	}

	return found
}

// skills tells the model of the skills that apply in dir, those in the
// places the user's plugins have among them.
func skills(ctx context.Context, dir string, plugins []string) string {
	found := FindSkills(ctx, dir, plugins).Found

	if len(found) == 0 {
		return ""
	}

	parts := []string{skillsIntro}

	for _, each := range found {
		parts = append(parts, fmt.Sprintf("[%s]\n%s: %s", each.Path, each.Name, each.Description))
	}

	return strings.Join(parts, "\n\n")
}

// readSkills reads the skills in one directory, each in a subdirectory of
// its own.
func readSkills(place string) ([]Skill, []LeftOut) {
	entries, err := os.ReadDir(place)

	if err != nil {
		return nil, nil
	}

	var found []Skill
	var left []LeftOut

	for _, entry := range entries {
		directory := filepath.Join(place, entry.Name())

		// Stat follows a link, so a skill linked from elsewhere counts.
		if info, err := os.Stat(directory); err != nil || !info.IsDir() {
			continue
		}

		path := filepath.Join(directory, "SKILL.md")
		raw, err := os.ReadFile(path)

		if errors.Is(err, fs.ErrNotExist) {
			left = append(left, LeftOut{Path: directory, Reason: "it has no SKILL.md"})
			continue
		}

		if err != nil {
			if failure, ok := errors.AsType[*fs.PathError](err); ok {
				err = failure.Err
			}

			left = append(left, LeftOut{Path: path, Reason: "it cannot be read: " + err.Error()})
			continue
		}

		skill, reason := parseSkill(string(raw), entry.Name())

		if reason != "" {
			left = append(left, LeftOut{Path: path, Reason: reason})
			continue
		}

		skill.Path = path
		found = append(found, skill)
	}

	return found, left
}

// parseSkill reads the frontmatter of a SKILL.md, or says why the skill is
// left out. One that says nothing of what it is for is, since the model
// could not tell when to use it.
func parseSkill(text, directory string) (Skill, string) {
	front, reason := frontmatter(text)

	if reason != "" {
		return Skill{}, reason
	}

	name, description, err := decodeFrontmatter(front)

	if err != nil {
		var retried error

		// Many skills leave a value with a colon in it unquoted, which YAML
		// rejects; such values are quoted and the frontmatter read again.
		// What is wrong is said of the file as it is.
		if name, description, retried = decodeFrontmatter(quotePlain(front)); retried != nil {
			return Skill{}, yamlProblem(front, err)
		}
	}

	if description == "" {
		return Skill{}, "its frontmatter has no description, which says when to use it"
	}

	if name == "" {
		name = directory
	}

	return Skill{Name: name, Description: shortened(description)}, ""
}

func decodeFrontmatter(front string) (name, description string, err error) {
	var meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}

	err = yaml.Unmarshal([]byte(front), &meta)

	return strings.TrimSpace(meta.Name), strings.TrimSpace(meta.Description), err
}

// yamlProblem says what YAML finds wrong with a frontmatter, on a line of
// the file. YAML's own line is not always the one to fix: it can be one too
// early, or where the mapping holding the mistake starts.
func yamlProblem(front string, err error) string {
	// Values of the wrong type are looked for first, since errors.As also
	// finds the first of them as a LoadError, which is what a syntax error is.
	if loaded, ok := errors.AsType[*yaml.LoadErrors](err); ok && len(loaded.Errors) > 0 {
		first := loaded.Errors[0]

		if first.Mark.Line == 0 {
			return "its frontmatter cannot be read: " + first.Message
		}

		return fmt.Sprintf("its frontmatter cannot be read: line %d: %s", first.Mark.Line, first.Message)
	}

	if syntax, ok := errors.AsType[*yaml.LoadError](err); ok {
		return fmt.Sprintf("its frontmatter is not valid YAML: line %d: %s", brokenLine(front), syntax.Message)
	}

	return "its frontmatter cannot be read: " + err.Error()
}

// brokenLine returns the line of front where it stops being valid YAML: the
// one after the longest start of it that YAML accepts. Only the syntax is
// checked, so what a value holds does not move it.
func brokenLine(front string) int {
	lines := strings.Split(front, "\n")
	valid := 0

	for count := 1; count <= len(lines); count++ {
		var node yaml.Node

		if yaml.Unmarshal([]byte(strings.Join(lines[:count], "\n")), &node) == nil {
			valid = count
		}
	}

	return valid + 1
}

// frontmatter returns the YAML that opens a SKILL.md, from its first "---"
// line up to the next one. The first line is kept, so the lines YAML speaks
// of are those of the file.
func frontmatter(text string) (string, string) {
	text = strings.TrimPrefix(text, "\ufeff")
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")

	if strings.TrimRight(lines[0], " \t") != "---" {
		return "", "it does not open with a frontmatter between --- lines"
	}

	for index := 1; index < len(lines); index++ {
		if strings.TrimRight(lines[index], " \t") == "---" {
			return strings.Join(lines[:index], "\n"), ""
		}
	}

	return "", "its frontmatter has no closing --- line"
}

// quotePlain quotes each top-level value written without quotes that holds
// ": ", which YAML takes for the start of a mapping. The indented lines that
// continue such a value are joined to it with spaces, as YAML folds them.
func quotePlain(front string) string {
	lines := strings.Split(front, "\n")
	var out []string

	for index := 0; index < len(lines); index++ {
		key, value, found := strings.Cut(lines[index], ": ")
		value = strings.TrimSpace(value)

		if !found || !plainKey(key) || !needsQuotes(value) {
			out = append(out, lines[index])
			continue
		}

		for index+1 < len(lines) && continues(lines[index+1]) {
			index++
			value += " " + strings.TrimSpace(lines[index])
		}

		// A Go quoted string is also a valid YAML double-quoted one.
		out = append(out, key+": "+strconv.Quote(value))
	}

	return strings.Join(out, "\n")
}

// plainKey reports whether key is one of the frontmatter's own, at the top
// level.
func plainKey(key string) bool {
	return key != "" && !strings.ContainsAny(key, " \t\"'#")
}

// needsQuotes reports whether value is written without quotes, and holds
// what YAML reads as a mapping inside it.
func needsQuotes(value string) bool {
	if value == "" || strings.ContainsAny(value[:1], "\"'|>[{&*!%@`#") {
		return false
	}

	return strings.Contains(value, ": ") || strings.HasSuffix(value, ":")
}

// continues reports whether line goes on with the value before it.
func continues(line string) bool {
	return strings.TrimSpace(line) != "" && (line[0] == ' ' || line[0] == '\t')
}

// shortened cuts a description longer than the format allows. The whole of
// it is still in the SKILL.md.
func shortened(description string) string {
	if len(description) <= maxDescriptionBytes {
		return description
	}

	return strings.TrimSpace(strings.ToValidUTF8(description[:maxDescriptionBytes], "")) + "…"
}
