package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/LiasCode/rumbo/internal/store"
	"github.com/LiasCode/rumbo/protocol"
)

// shownValues is a list of values as value:label pairs.
func shownValues(values []protocol.Value) string {
	var shown []string

	for _, value := range values {
		shown = append(shown, value.Value+":"+value.Label)
	}

	return strings.Join(shown, " | ")
}

// markedValues is a list of values as value:mark pairs, the mark being what
// comes first in a value's description, before what describes it.
func markedValues(values []protocol.Value) string {
	var shown []string

	for _, value := range values {
		mark, _, _ := strings.Cut(value.Description, "; ")
		shown = append(shown, value.Value+":"+mark)
	}

	return strings.Join(shown, " | ")
}

// The models of a provider come from the provider, which lists them with a
// function of its own, and the one the session uses is marked; another
// provider can be asked by name.
func TestTheModelsOfAProviderAreAskedOfIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}

		w.Write([]byte(`{"data":[{"id":"gpt-a"},{"id":"gpt-b"}]}`))
	}))
	defer server.Close()

	t.Setenv("RUMBO_TEST_KEY", "test-key")

	h := testHost(t)
	config, _ := json.Marshal(map[string]string{"base_url": server.URL, "api_key_env": "RUMBO_TEST_KEY"})
	h.database.PutProgram(store.KindProvider, store.Program{Name: "mine", Command: []string{selfPlaceholder, "provide", "openai"}, Config: config})
	t.Cleanup(h.close)

	call(t, h, "use", `{"provider":"mine","model":"gpt-b"}`)
	call(t, h, "provider-set", `{"provider":"mine","change":"model=gpt-a"}`)

	listed := call(t, h, "provider-models", "")

	// A mark goes in the description, so a label stays the name a person
	// knows a model by.
	if got := markedValues(listed.Values); got != "gpt-a:the one mine starts with | gpt-b:in use in this session" {
		t.Fatalf("models = %q", got)
	}

	if got := shownValues(listed.Values); got != "gpt-a: | gpt-b:" {
		t.Fatalf("labels = %q", got)
	}

	// The command line lists them too, without a host.
	if got, err := listModels(h.database, "mine", io.Discard); err != nil || got != "gpt-a  the one mine starts with\ngpt-b" {
		t.Fatalf("listModels = %q, err = %v", got, err)
	}

	if named := call(t, h, "provider-models", `{"provider":"mine"}`); len(named.Values) != 2 {
		t.Fatalf("models of a provider named = %+v", named.Values)
	}

	if err := refused(t, h, "provider-models", `{"provider":"missing"}`); !strings.Contains(err, "not registered") {
		t.Fatalf("models of a provider not registered: %v", err)
	}

	// The use function says where its models come from, for a front end to
	// pass the provider given before them.
	use := hostFunctions[slices.IndexFunc(hostFunctions, func(function hostFunction) bool { return function.Name == "use" })]

	if use.Params[1].Values != "provider-models" {
		t.Fatalf("the model of use lists no values: %+v", use.Params[1])
	}
}

// The values a setting takes come as name=value changes, with those in
// force marked, after the empty value of each one chosen at that level: the
// session's options, or a provider's settings.
func TestTheValuesOfSettingsAreListedAsChanges(t *testing.T) {
	h := testHost(t)
	withClaude(t, h.database, "")
	t.Cleanup(h.close)

	call(t, h, "use", `{"provider":"claude","model":"claude-opus-5-5"}`)
	call(t, h, "provider-set", `{"provider":"claude","change":"effort=high"}`)

	efforts := " | effort=low: | effort=medium: | effort=high:in force | effort=xhigh: | effort=max:"

	if got := markedValues(call(t, h, "session-values", "").Values); got != strings.TrimPrefix(efforts, " | ") {
		t.Fatalf("the session's, with nothing chosen = %q", got)
	}

	call(t, h, "session-set", `{"change":"effort=low"}`)

	session := "effort=:empty: gives it back | effort=low:in force | effort=medium: | effort=high: | effort=xhigh: | effort=max:"

	if got := markedValues(call(t, h, "session-values", "").Values); got != session {
		t.Fatalf("the session's = %q, want %q", got, session)
	}

	// The models need the server, which is not there; the rest is listed.
	if got := markedValues(call(t, h, "provider-values", `{"provider":"claude"}`).Values); got != "base_url=:in force: http://127.0.0.1:9 | effort=:empty: gives it back"+efforts {
		t.Fatalf("the provider's = %q", got)
	}

	// No mark is a label, which a front end may show in place of the value.
	for _, value := range call(t, h, "provider-values", `{"provider":"claude"}`).Values {
		if value.Label != "" {
			t.Errorf("%s is labelled %q", value.Value, value.Label)
		}
	}
}

// Fixed values and booleans are listed without asking the provider.
func TestChoicesOfAnOptionWithoutAFunction(t *testing.T) {
	for option, want := range map[*protocol.Option]string{
		{Type: protocol.OptionBoolean}:                          "true: | false:",
		{Type: protocol.OptionString, Enum: []string{"a", "b"}}: "a: | b:",
		{Type: protocol.OptionString}:                           "",
	} {
		values, err := choicesOf(context.Background(), nil, *option)

		if err != nil || shownValues(values) != want {
			t.Errorf("choicesOf(%+v) = %q, %v; want %q", *option, shownValues(values), err, want)
		}
	}
}
