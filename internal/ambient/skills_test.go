package ambient

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeSkill puts a SKILL.md in a directory of its own under skills.
func writeSkill(t *testing.T, skills, directory, content string) string {
	t.Helper()

	path := filepath.Join(skills, directory, "SKILL.md")

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	return path
}

// The model is told of the user's skills and of the project's, from its root
// down to the working directory, and the most specific one of a name wins.
func TestSkillsComeFromTheUserAndTheProject(t *testing.T) {
	config := userConfig(t)

	// The temporary directory may be reached through a symlink, and git
	// reports the real path.
	repository, _ := filepath.EvalSymlinks(t.TempDir())

	if output, err := exec.Command("git", "init", "--quiet", repository).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s", output)
	}

	dir := filepath.Join(repository, "pkg")
	user := filepath.Join(config, "rumbo", "skills")

	pdf := writeSkill(t, user, "pdf", "---\nname: pdf\ndescription: Read and fill PDF forms.\n---\n# PDF\n")
	replaced := writeSkill(t, user, "review", "---\nname: review\ndescription: The user's way to review.\n---\n")
	review := writeSkill(t, filepath.Join(repository, ".agents", "skills"), "review", "---\nname: review\ndescription: This project's way to review.\n---\n")
	deploy := writeSkill(t, filepath.Join(dir, ".agents", "skills"), "deploy", "---\nname: deploy\ndescription: >\n  Deploy the service\n  to staging.\n---\n")

	got := skills(context.Background(), dir)
	want := skillsIntro + "\n\n" +
		"[" + deploy + "]\ndeploy: Deploy the service to staging.\n\n" +
		"[" + pdf + "]\npdf: Read and fill PDF forms.\n\n" +
		"[" + review + "]\nreview: This project's way to review."

	if got != want {
		t.Fatalf("skills section:\n%s\n\nwant:\n%s", got, want)
	}

	found := FindSkills(context.Background(), dir)
	places := []string{user, filepath.Join(repository, ".agents", "skills"), filepath.Join(dir, ".agents", "skills")}

	if !slices.Equal(found.Places, places) {
		t.Errorf("looked in %v, want %v", found.Places, places)
	}

	if len(found.Left) != 1 || found.Left[0].Path != replaced || found.Left[0].Reason != "replaced by "+review+", a skill of the same name" {
		t.Errorf("the user's review is left out, saying which replaces it; got %+v", found.Left)
	}
}

// A skill the model could not tell when to use is left out, saying why, and
// without skills there is no section, so nothing more is sent.
func TestSkillsWithoutADescriptionAreLeftOut(t *testing.T) {
	userConfig(t)

	dir := t.TempDir()
	ctx := context.Background()
	place := filepath.Join(dir, ".agents", "skills")

	plain := writeSkill(t, place, "plain", "# No frontmatter\n")
	empty := writeSkill(t, place, "empty", "---\nname: empty\n---\n")
	broken := writeSkill(t, place, "broken", "---\nname: [broken\ndescription: x\n---\n")
	open := writeSkill(t, place, "open", "---\nname: open\ndescription: Never closed.\n")
	os.MkdirAll(filepath.Join(place, "scripts"), 0o755)
	os.WriteFile(filepath.Join(place, "notes.md"), []byte("not a skill"), 0o644)

	if got := skills(ctx, dir); got != "" {
		t.Fatalf("no usable skill, yet the section is %q", got)
	}

	reasons := map[string]string{}

	for _, left := range FindSkills(ctx, dir).Left {
		reasons[left.Path] = left.Reason
	}

	for path, want := range map[string]string{
		broken:                          "its frontmatter is not valid YAML: line 2: did not find expected ',' or ']'",
		empty:                           "its frontmatter has no description",
		open:                            "its frontmatter has no closing --- line",
		plain:                           "it does not open with a frontmatter",
		filepath.Join(place, "scripts"): "it has no SKILL.md",
	} {
		if !strings.HasPrefix(reasons[path], want) {
			t.Errorf("%s is left out because %q, want %q", path, reasons[path], want)
		}
	}

	if len(reasons) != 5 {
		t.Errorf("left out: %v; a file beside the skills is not one", reasons)
	}

	unnamed := writeSkill(t, place, "unnamed", "\ufeff---\r\ndescription: Named after its directory.\r\n---\r\n")

	if got := skills(ctx, dir); !strings.HasSuffix(got, "["+unnamed+"]\nunnamed: Named after its directory.") || strings.Count(got, "[/") != 1 {
		t.Fatalf("a skill without a name takes its directory's, got %q", got)
	}
}

// Many skills leave a value with a colon unquoted, which YAML rejects; they
// are read anyway, and what YAML reads as it is stays as YAML reads it.
func TestSkillsWithUnquotedColonsAreRead(t *testing.T) {
	skill, reason := parseSkill("---\nname: review\ndescription: Use when: the user asks\n  for a review.\ncompatibility: Needs: git\n---\n", "review")

	if reason != "" || skill.Name != "review" || skill.Description != "Use when: the user asks for a review." {
		t.Fatalf("unquoted colons: %+v, %q", skill, reason)
	}

	skill, reason = parseSkill("---\nname: notes\ndescription: |\n  First: this.\n  Then that.\nallowed-tools: Bash(git:*)\n---\n", "notes")

	if reason != "" || skill.Description != "First: this.\nThen that." {
		t.Fatalf("a block value: %+v, %q", skill, reason)
	}

	if _, reason := parseSkill("---\ndescription: a: b\nname: [broken\n---\n", "x"); reason != "its frontmatter is not valid YAML: line 2: mapping values are not allowed in this context" {
		t.Fatalf("what is wrong is said of the file as it is, got %q", reason)
	}
}

// What YAML finds wrong is said on the line of the file to fix, which is
// not always the line YAML gives.
func TestSkillMistakesAreSaidOnTheirLine(t *testing.T) {
	for text, want := range map[string]string{
		// YAML says line 1, one too early.
		"---\nname: [broken\ndescription: x\n---\n": "line 2: did not find expected ',' or ']'",
		// YAML says line 1, where the mapping starts.
		"---\nname: x\n- item\ndescription: y\n---\n": "line 3: did not find expected key",
		// YAML says line 2.
		"---\nname: x\n\tdescription: y\n---\n": "line 3: found a tab character that violates indentation",
		// What a value holds is not a mistake of syntax.
		"---\nname: [a]\ndescription: y\nk: v\nbad: : x\n---\n": "line 5: mapping values are not allowed in this context",
	} {
		_, reason := parseSkill(text, "x")

		if reason != "its frontmatter is not valid YAML: "+want {
			t.Errorf("%q is left out because %q, want %q", text, reason, want)
		}
	}

	if _, reason := parseSkill("---\nname: x\ndescription: [a, b]\n---\n", "x"); !strings.HasPrefix(reason, "its frontmatter cannot be read: line 3: cannot construct !!seq") {
		t.Errorf("a description that is a list: %q", reason)
	}
}

// A description longer than the format allows is cut, since it is sent
// with every request.
func TestLongSkillDescriptionsAreCut(t *testing.T) {
	long := strings.Repeat("é", maxDescriptionBytes)
	got := shortened(long)

	if len(got) > maxDescriptionBytes+len("…") || !strings.HasSuffix(got, "é…") {
		t.Fatalf("cut to %d bytes, ending %q", len(got), got[len(got)-10:])
	}

	if short := shortened("Brief."); short != "Brief." {
		t.Fatalf("a short description was changed: %q", short)
	}
}
