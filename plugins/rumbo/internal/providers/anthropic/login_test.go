package anthropic

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

	"github.com/LiasCode/rumbo/plugins/rumbo/internal/credentials"
	"github.com/LiasCode/rumbo/protocol"
)

// testHandler is a handler whose keys are kept in a directory of the test,
// never in the user's.
func testHandler(t *testing.T) *Handler {
	t.Helper()

	handler := New()
	handler.keys = credentials.Store{Path: filepath.Join(t.TempDir(), "anthropic.json")}

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

func TestLoginStoresAKeyThatWorks(t *testing.T) {
	// Nothing of the machine's own credentials is read.
	for _, name := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_PROFILE", "ANTHROPIC_BASE_URL"} {
		t.Setenv(name, "")
	}

	t.Setenv("RUMBO_HOME", t.TempDir())

	var sent string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization := r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == "/v1/models" && authorization == "Bearer good-key":
			fmt.Fprint(w, `{"data":[{"id":"claude-opus-5-5","display_name":"Claude Opus 5.5","type":"model"}],"has_more":false}`)

		case r.URL.Path == "/v1/models":
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)

		case r.URL.Path == "/v1/messages":
			sent = authorization
			w.Header().Set("Content-Type", "text/event-stream")

			for _, event := range []string{
				`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`,
				`{"type":"message_stop"}`,
			} {
				var header struct {
					Type string `json:"type"`
				}

				json.Unmarshal([]byte(event), &header)
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", header.Type, event)
			}

		default:
			http.NotFound(w, r)
		}
	}))

	defer server.Close()

	handler := testHandler(t)
	config, _ := json.Marshal(Config{BaseURL: server.URL})
	ctx := context.Background()

	if _, err := handler.Initialize(ctx, protocol.InitializeParams{Config: config}); err != nil {
		t.Fatal(err)
	}

	call := func(name string, person *answering) (string, error) {
		result, err := handler.InvokeDialog(ctx, name, nil, person)
		text, _ := result.(string)

		return text, err
	}

	if text, _ := call("account", nil); !strings.Contains(text, "no key in the environment or stored by login") {
		t.Fatalf("account before login = %q", text)
	}

	// A key the API refuses is not stored.
	if _, err := call("login", &answering{answer: "bad-key"}); err == nil || !strings.Contains(err.Error(), "the key was not stored") {
		t.Fatalf("login with a bad key: err = %v", err)
	}

	if stored, _ := handler.keys.APIKey(server.URL); stored != "" {
		t.Fatalf("a refused key was stored: %q", stored)
	}

	person := &answering{answer: "  good-key\n"}

	if text, err := call("login", person); err != nil || !strings.Contains(text, "logged in to "+server.URL) || len(person.said) == 0 {
		t.Fatalf("login = %q, %v; said %v", text, err, person.said)
	}

	if text, _ := call("account", nil); !strings.Contains(text, "using the key login stored") {
		t.Fatalf("account after login = %q", text)
	}

	complete := func() {
		params := protocol.CompleteParams{
			Model:    "claude-opus-5-5",
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: []protocol.Block{{Type: protocol.BlockText, Text: "hi"}}}},
		}

		if _, err := handler.Complete(ctx, params, func(string) {}); err != nil {
			t.Fatal(err)
		}
	}

	complete()

	if sent != "Bearer good-key" {
		t.Fatalf("a request after login was sent with %q", sent)
	}

	// A process started with a key in its environment uses that one.
	t.Setenv("ANTHROPIC_API_KEY", "env-key")

	started := testHandler(t)
	started.keys = handler.keys

	if _, err := started.Initialize(ctx, protocol.InitializeParams{Config: config}); err != nil {
		t.Fatal(err)
	}

	// call and complete go to the new process from here on.
	handler = started
	complete()

	if text, _ := call("account", nil); sent != "Bearer env-key" || !strings.Contains(text, "ANTHROPIC_API_KEY from the environment, which comes before") {
		t.Fatalf("with a key in the environment: sent %q, account %q", sent, text)
	}

	if text, _ := call("logout", nil); !strings.Contains(text, "is removed") {
		t.Fatalf("logout = %q", text)
	}

	if text, _ := call("logout", nil); !strings.Contains(text, "no key is stored") {
		t.Fatalf("a second logout = %q", text)
	}
}
