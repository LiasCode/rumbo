package tui

import (
	"encoding/json"
	"reflect"
	"testing"

	"rumbo/protocol"
)

func TestOnlyASlashAndAWordIsACommand(t *testing.T) {
	for line, want := range map[string]bool{
		"/help":                  true,
		"/open 4cf0261c":         true,
		"/etc/hosts explain it":  false,
		"/":                      false,
		"explain /help":          false,
		"/Users/me/file.txt fix": false,
	} {
		if got := isCommand(line); got != want {
			t.Errorf("isCommand(%q) = %v, want %v", line, got, want)
		}
	}
}

func TestCompletionOfAWord(t *testing.T) {
	commands := []string{"/help", "/sessions", "/set", "/status"}

	if head, token := splitLast("/open 4c"); head != "/open " || token != "4c" {
		t.Fatalf("splitLast = %q, %q", head, token)
	}

	if matches := narrow(commands, "/s"); !reflect.DeepEqual(matches, []string{"/sessions", "/set", "/status"}) {
		t.Fatalf("narrow = %v", matches)
	}

	// Several candidates are completed as far as they agree.
	if prefix := commonPrefix([]string{"/sessions", "/set"}); prefix != "/se" {
		t.Fatalf("commonPrefix = %q", prefix)
	}

	if prefix := commonPrefix([]string{"claude-opus-5-5", "claude-opus-5"}); prefix != "claude-opus-5" {
		t.Fatalf("commonPrefix = %q", prefix)
	}
}

// Arguments typed after a command become the function's parameters, in its
// order, in the types it declares; the last one takes the rest of the line.
func TestArgumentsFollowTheFunctionsDescription(t *testing.T) {
	function := protocol.Function{Name: "search", Params: []protocol.Option{
		{Name: "limit", Type: protocol.OptionNumber},
		{Name: "exact", Type: protocol.OptionBoolean},
		{Name: "query", Type: protocol.OptionString},
	}}

	raw, err := arguments(function, []string{"5", "true", "two", "words"})

	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	json.Unmarshal(raw, &got)

	if want := map[string]any{"limit": 5.0, "exact": true, "query": "two words"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %v, want %v", got, want)
	}

	if _, err := arguments(function, []string{"5"}); err == nil {
		t.Fatal("missing arguments were accepted")
	}

	if _, err := arguments(function, []string{"many", "true", "x"}); err == nil {
		t.Fatal("a word was accepted as a number")
	}
}
