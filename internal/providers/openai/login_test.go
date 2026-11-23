package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LiasCode/rumbo/internal/credentials"
	"github.com/LiasCode/rumbo/protocol"
)

// testHandler is a handler whose keys are kept in a directory of the test,
// never in the user's.
func testHandler(t *testing.T) *Handler {
	t.Helper()

	handler := New()
	handler.keys = credentials.Store{Path: filepath.Join(t.TempDir(), "openai.json")}

	return handler
}

// answering is the person behind a function's questions.
type answering struct {
	answer string
	said   []string
}

func (a *answering) Say(text string) {
	a.said = append(a.said, text)
}

func (a *answering) Ask(_ context.Context, _ string, secret bool) (string, error) {
	if !secret {
		return "", errors.New("the key was asked for in the open")
	}

	return a.answer, nil
}

func TestLoginStoresAKeyForItsServer(t *testing.T) {
	t.Setenv("RUMBO_TEST_KEY", "")

	var sent string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization := r.Header.Get("Authorization")

		switch {
		case r.URL.Path == "/models" && authorization == "Bearer good-key":
			fmt.Fprint(w, `{"object":"list","data":[{"id":"local"}]}`)

		case r.URL.Path == "/models":
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"Incorrect API key provided"}}`)

		case r.URL.Path == "/chat/completions":
			sent = authorization
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")

		default:
			http.NotFound(w, r)
		}
	}))

	defer server.Close()

	handler := testHandler(t)
	ctx := context.Background()

	// The address is kept without a trailing slash, so both spellings of it
	// find the same key.
	config, _ := json.Marshal(Config{BaseURL: server.URL + "/", APIKeyEnv: "RUMBO_TEST_KEY"})

	if _, err := handler.Initialize(ctx, protocol.InitializeParams{Config: config}); err != nil {
		t.Fatal(err)
	}

	call := func(name string, person *answering) (string, error) {
		result, err := handler.InvokeDialog(ctx, name, nil, person)
		text, _ := result.(string)

		return text, err
	}

	complete := func() {
		params := protocol.CompleteParams{
			Model:    "local",
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: []protocol.Block{{Type: protocol.BlockText, Text: "hi"}}}},
		}

		if _, err := handler.Complete(ctx, params, func(string) {}); err != nil {
			t.Fatal(err)
		}
	}

	// Without a key, requests go without one, as local servers expect.
	complete()

	if text, _ := call("account", nil); sent != "" || !strings.Contains(text, "requests go without one") {
		t.Fatalf("without a key: sent %q, account %q", sent, text)
	}

	if _, err := call("login", &answering{answer: "bad-key"}); err == nil || !strings.Contains(err.Error(), "the key was not stored") {
		t.Fatalf("login with a bad key: err = %v", err)
	}

	if text, err := call("login", &answering{answer: "good-key"}); err != nil || !strings.Contains(text, "logged in to "+server.URL) {
		t.Fatalf("login = %q, %v", text, err)
	}

	complete()

	if text, _ := call("account", nil); sent != "Bearer good-key" || !strings.Contains(text, "using the key login stored") {
		t.Fatalf("after login: sent %q, account %q", sent, text)
	}

	// The environment comes first.
	t.Setenv("RUMBO_TEST_KEY", "env-key")
	complete()

	if text, _ := call("account", nil); sent != "Bearer env-key" || !strings.Contains(text, "RUMBO_TEST_KEY from the environment, which comes before") {
		t.Fatalf("with a key in the environment: sent %q, account %q", sent, text)
	}

	if text, _ := call("logout", nil); !strings.Contains(text, "is removed") {
		t.Fatalf("logout = %q", text)
	}

	if stored, _ := handler.keys.APIKey(server.URL); stored != "" {
		t.Fatalf("a key is left after logout: %q", stored)
	}
}
