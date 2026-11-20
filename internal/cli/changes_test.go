package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LiasCode/rumbo/protocol"
)

// A change is name=value at every level, each name once, and an empty value
// gives a setting back.
func TestChangesAreNameEqualsValue(t *testing.T) {
	changes, err := parseChanges([]string{"effort=high", "note=two words", "base_url=", "query=a=b"})

	if err != nil || len(changes) != 4 || changes[1].value != "two words" || !changes[2].back() || changes[3].value != "a=b" {
		t.Fatalf("changes = %+v, err = %v", changes, err)
	}

	for _, written := range [][]string{nil, {"effort"}, {"=high"}, {"effort=high", "effort=low"}} {
		if _, err := parseChanges(written); err == nil {
			t.Errorf("%q was taken", written)
		}
	}
}

// A value is read as what describes its setting asks: a number stays the
// number it was written as, text stays text even when it looks like a
// number, and a setting nothing describes is read as JSON when it is JSON.
func TestValuesAreReadByWhatDescribesThem(t *testing.T) {
	for _, test := range []struct {
		option protocol.Option
		text   string
		want   string
	}{
		{protocol.Option{Name: "context_window", Type: protocol.OptionNumber}, "32768", `32768`},
		{protocol.Option{Name: "temperature", Type: protocol.OptionNumber}, "0.5", `0.5`},
		{protocol.Option{Name: "effort", Type: protocol.OptionString}, "3", `"3"`},
		{protocol.Option{Name: "api", Type: protocol.OptionString, Enum: []string{"chat", "responses"}}, "chat", `"chat"`},
		{protocol.Option{Name: "stream", Type: protocol.OptionBoolean}, "true", `true`},
		{protocol.Option{Name: "loose"}, "3", `3`},
		{protocol.Option{Name: "loose"}, "two words", `"two words"`},
	} {
		if got, err := encodeValue(test.option, test.text); err != nil || string(got) != test.want {
			t.Errorf("%s=%s is %s, %v; want %s", test.option.Name, test.text, got, err, test.want)
		}
	}

	for _, test := range []struct {
		option protocol.Option
		text   string
		said   string
	}{
		{protocol.Option{Name: "context_window", Type: protocol.OptionNumber}, "big", `context_window is a number, not "big"`},
		{protocol.Option{Name: "context_window", Type: protocol.OptionNumber}, `"3"`, "is a number"},
		{protocol.Option{Name: "api", Type: protocol.OptionString, Enum: []string{"chat", "responses"}}, "bogus", `api is one of chat, responses, not "bogus"`},
		{protocol.Option{Name: "stream", Type: protocol.OptionBoolean}, "yes", `stream is true or false, not "yes"`},
	} {
		if _, err := encodeValue(test.option, test.text); err == nil || !strings.Contains(err.Error(), test.said) {
			t.Errorf("%s=%s: err = %v, want %q", test.option.Name, test.text, err, test.said)
		}
	}

	// What a configuration in force holds says its type, for a field the
	// provider does not describe.
	for value, kind := range map[string]string{`"x"`: protocol.OptionString, `0`: protocol.OptionNumber, `-1.5`: protocol.OptionNumber, `false`: protocol.OptionBoolean, `{}`: ""} {
		if got := typeOf("field", json.RawMessage(value)); got.Type != kind {
			t.Errorf("typeOf(%s) = %q, want %q", value, got.Type, kind)
		}
	}
}

// The options chosen in a session are checked against those the provider
// describes, and laid over what the session chose before.
func TestOptionsAreCheckedAgainstTheProvider(t *testing.T) {
	described := []protocol.Option{{Name: "effort", Type: protocol.OptionString}, {Name: "web", Type: protocol.OptionBoolean}}

	options, said, err := chooseOptions("claude", described, json.RawMessage(`{"web":true}`), []string{"effort=3", "web="})

	if err != nil || string(options) != `{"effort":"3"}` || strings.Join(said, "|") != "effort = 3|web: given back to claude's" {
		t.Fatalf("options = %s, said %q, err = %v", options, said, err)
	}

	if none, _, err := chooseOptions("claude", described, json.RawMessage(`{"web":true}`), []string{"web="}); err != nil || none != nil {
		t.Fatalf("with nothing left chosen: %s, err = %v", none, err)
	}

	if _, _, err := chooseOptions("toy", nil, nil, []string{"depth=3"}); err == nil || !strings.Contains(err.Error(), `toy has no option "depth"; toy describes no options`) {
		t.Fatalf("an option of a provider that describes none: err = %v", err)
	}
}
