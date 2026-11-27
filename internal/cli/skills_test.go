package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LiasCode/rumbo/internal/ambient"
)

// The skills function tells the person which skills the model is told of
// in the session's directory, and why one is left out, so a skill that
// does not work can be fixed.
func TestSkillsSayWhatTheModelIsToldOf(t *testing.T) {
	t.Setenv("RUMBO_HOME", t.TempDir())
	data := t.TempDir()
	t.Setenv("RUMBO_DATA", data)

	h := testHost(t)
	dir := t.TempDir()
	h.dir = dir
	place := filepath.Join(dir, ".agents", "skills")

	// rumbo's own skill is told of wherever the session is.
	alone := call(t, h, "skills", "").Text
	builtin := "  rumbo: " + skillDescription(t) + "\n" +
		"    " + filepath.Join(data, "builtin", "skills", "rumbo", "SKILL.md") + "\n"

	if alone != "the model is told of these skills:\n"+strings.TrimRight(builtin, "\n") {
		t.Fatalf("with only rumbo's own skill, got:\n%s", alone)
	}

	for name, content := range map[string]string{
		"review": "---\nname: review\ndescription: Use when: the user asks\n  for a review.\n---\n",
		"broken": "---\ndescription: Never closed.\n",
	} {
		os.MkdirAll(filepath.Join(place, name), 0o755)
		os.WriteFile(filepath.Join(place, name, "SKILL.md"), []byte(content), 0o644)
	}

	want := "the model is told of these skills:\n" +
		"  review: Use when: the user asks for a review.\n" +
		"    " + filepath.Join(place, "review", "SKILL.md") + "\n" + builtin +
		"left out:\n" +
		"  " + filepath.Join(place, "broken", "SKILL.md") + "\n" +
		"    its frontmatter has no closing --- line"

	if got := call(t, h, "skills", "").Text; got != want {
		t.Fatalf("skills:\n%s\n\nwant:\n%s", got, want)
	}
}

// skillDescription is the description of rumbo's own skill.
func skillDescription(t *testing.T) string {
	t.Helper()

	found := ambient.FindSkills(t.TempDir(), []string{filepath.Join(os.Getenv("RUMBO_DATA"), "builtin", "skills")}).Found

	if len(found) != 1 {
		t.Fatalf("rumbo's own skills: %+v", found)
	}

	return found[0].Description
}
