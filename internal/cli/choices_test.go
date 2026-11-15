package cli

import (
	"context"
	"encoding/json"
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

	listed := call(t, h, "provider-models", "")

	if got := shownValues(listed.Values); got != "gpt-a: | gpt-b:in use in this session" {
		t.Fatalf("models = %q", got)
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

// The values of a provider's options come as name=value changes, each
// option's empty one first, with those in force marked: the session's, or
// those stored for a provider named.
func TestTheValuesOfOptionsAreListedAsChanges(t *testing.T) {
	h := testHost(t)
	h.database.PutProgram(store.KindProvider, store.Program{Name: "claude", Command: []string{selfPlaceholder, "provide", "anthropic"}})
	t.Cleanup(h.close)

	call(t, h, "use", `{"provider":"claude","model":"claude-opus-5-5"}`)
	call(t, h, "session-options", `{"change":"effort=low"}`)
	call(t, h, "provider-options", `{"provider":"claude","change":"effort=high"}`)

	session := "effort=:empty: removes it | effort=low:in force | effort=medium: | effort=high: | effort=xhigh: | effort=max:"

	if got := shownValues(call(t, h, "option-values", "").Values); got != session {
		t.Fatalf("the session's = %q, want %q", got, session)
	}

	stored := "effort=:empty: removes it | effort=low: | effort=medium: | effort=high:in force | effort=xhigh: | effort=max:"

	if got := shownValues(call(t, h, "option-values", `{"provider":"claude"}`).Values); got != stored {
		t.Fatalf("the provider's = %q, want %q", got, stored)
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
