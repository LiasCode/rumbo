package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The skills function tells the person which skills the model is told of
// in the session's directory, and why one is left out, so a skill that
// does not work can be fixed.
func TestSkillsSayWhatTheModelIsToldOf(t *testing.T) {
	t.Setenv("RUMBO_HOME", t.TempDir())

	h := testHost(t)
	dir := t.TempDir()
	h.dir = dir
	place := filepath.Join(dir, ".agents", "skills")

	if empty := call(t, h, "skills", ""); !strings.HasPrefix(empty.Text, "no skills here") || !strings.Contains(empty.Text, "\n  "+place) {
		t.Fatalf("without skills it says where they go, got:\n%s", empty.Text)
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
		"    " + filepath.Join(place, "review", "SKILL.md") + "\n" +
		"left out:\n" +
		"  " + filepath.Join(place, "broken", "SKILL.md") + "\n" +
		"    its frontmatter has no closing --- line"

	if got := call(t, h, "skills", "").Text; got != want {
		t.Fatalf("skills:\n%s\n\nwant:\n%s", got, want)
	}
}
