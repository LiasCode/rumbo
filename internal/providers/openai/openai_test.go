package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LiasCode/rumbo/protocol"
)

func TestCompleteStreamsTextAndToolCalls(t *testing.T) {
	var received chatRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)

		chunks := []string{
			`{"choices":[{"delta":{"content":"Let me "}}]}`,
			`{"choices":[{"delta":{"content":"look."}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"bash","arguments":"{\"comm"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"and\":\"ls\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":60}}}`,
			`[DONE]`,
		}

		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
	}))

	defer server.Close()

	handler := testHandler(t)
	config, _ := json.Marshal(Config{BaseURL: server.URL, APIKeyEnv: "RUMBO_TEST_NO_KEY"})

	if _, err := handler.Initialize(context.Background(), protocol.InitializeParams{Config: config}); err != nil {
		t.Fatal(err)
	}

	var streamed strings.Builder

	result, err := handler.Complete(context.Background(), protocol.CompleteParams{
		Model:  "local",
		System: "be brief",
		Messages: []protocol.Message{
			{Role: protocol.RoleUser, Content: []protocol.Block{{Type: protocol.BlockText, Text: "list files"}}},
			{Role: protocol.RoleAssistant, Content: []protocol.Block{{Type: protocol.BlockToolCall, ID: "c0", Name: "bash", Args: json.RawMessage(`{"command":"pwd"}`)}}},
			{Role: protocol.RoleUser, Content: []protocol.Block{
				{Type: protocol.BlockToolResult, ID: "c0", Text: "/tmp"},
				{Type: protocol.BlockText, Text: "and now?"},
			}},
		},
	}, func(text string) { streamed.WriteString(text) })

	if err != nil {
		t.Fatal(err)
	}

	roles := ""

	for _, message := range received.Messages {
		roles += message.Role + " "
	}

	if roles != "system user assistant tool user " {
		t.Fatalf("request roles = %q", roles)
	}

	content := result.Message.Content

	if streamed.String() != "Let me look." || len(content) != 2 || content[0].Text != "Let me look." {
		t.Fatalf("text: streamed %q, content %+v", streamed.String(), content)
	}

	if content[1].ID != "c1" || content[1].Name != "bash" || string(content[1].Args) != `{"command":"ls"}` || result.Stop != protocol.StopToolCalls {
		t.Fatalf("tool call: %+v stop %s", content[1], result.Stop)
	}

	if result.Usage != (protocol.Usage{Input: 40, Output: 7, CacheRead: 60}) {
		t.Fatalf("usage = %+v", result.Usage)
	}
}

func TestTemporaryFailuresAreMarkedTransient(t *testing.T) {
	status := http.StatusTooManyRequests

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", status)
	}))

	handler := testHandler(t)
	config, _ := json.Marshal(Config{BaseURL: server.URL, APIKeyEnv: "RUMBO_TEST_NO_KEY"})
	handler.Initialize(context.Background(), protocol.InitializeParams{Config: config})

	complete := func() error {
		_, err := handler.Complete(context.Background(), protocol.CompleteParams{Model: "m"}, func(string) {})
		return err
	}

	if err := complete(); !protocol.IsTransient(err) {
		t.Fatalf("429: err = %v, want transient", err)
	}

	status = http.StatusUnauthorized

	if err := complete(); err == nil || protocol.IsTransient(err) {
		t.Fatalf("401: err = %v, want a permanent failure", err)
	}

	server.Close()

	if err := complete(); !protocol.IsTransient(err) {
		t.Fatalf("unreachable server: err = %v, want transient", err)
	}
}

func TestModelsFunctionListsWhatTheServerOffers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}

		fmt.Fprint(w, `{"object":"list","data":[{"id":"qwen3-coder"},{"id":"llama4"}]}`)
	}))

	defer server.Close()

	handler := testHandler(t)
	config, _ := json.Marshal(Config{BaseURL: server.URL, APIKeyEnv: "RUMBO_TEST_NO_KEY"})
	described, _ := handler.Initialize(context.Background(), protocol.InitializeParams{Config: config})

	if described.Model == nil || described.Model.Values != "models" {
		t.Fatalf("described = %+v", described)
	}

	result, err := handler.InvokeDialog(context.Background(), "models", nil, nil)

	if err != nil {
		t.Fatal(err)
	}

	if listed := result.(protocol.ValuesResult); len(listed.Values) != 2 || listed.Values[1].Value != "llama4" {
		t.Fatalf("models = %+v", listed)
	}
}

func TestConfiguredWindowAndOverflow(t *testing.T) {
	tooLong := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tooLong {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"This model's maximum context length is 8192 tokens.","code":"context_length_exceeded"}}`)

			return
		}

		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi.\"}}]}\n\ndata: [DONE]\n\n")
	}))

	defer server.Close()

	handler := testHandler(t)
	config, _ := json.Marshal(Config{BaseURL: server.URL, APIKeyEnv: "RUMBO_TEST_NO_KEY", ContextWindow: 8192})
	handler.Initialize(context.Background(), protocol.InitializeParams{Config: config})

	result, err := handler.Complete(context.Background(), protocol.CompleteParams{Model: "m"}, func(string) {})

	if err != nil || result.ContextWindow != 8192 {
		t.Fatalf("window = %d, err = %v", result.ContextWindow, err)
	}

	tooLong = true

	if _, err := handler.Complete(context.Background(), protocol.CompleteParams{Model: "m"}, func(string) {}); !protocol.IsOverflow(err) {
		t.Fatalf("a prompt that does not fit: err = %v, want an overflow", err)
	}
}

func TestEffortIsSentAsReasoningEffort(t *testing.T) {
	var received map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = nil
		json.NewDecoder(r.Body).Decode(&received)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))

	defer server.Close()

	complete := func(effort, chosen string) (any, error) {
		handler := testHandler(t)
		config, _ := json.Marshal(Config{BaseURL: server.URL, APIKeyEnv: "RUMBO_TEST_NO_KEY", Effort: effort})

		if _, err := handler.Initialize(context.Background(), protocol.InitializeParams{Config: config}); err != nil {
			t.Fatal(err)
		}

		params := protocol.CompleteParams{
			Model:    "reasoner",
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: []protocol.Block{{Type: protocol.BlockText, Text: "hi"}}}},
			Options:  json.RawMessage(chosen),
		}

		_, err := handler.Complete(context.Background(), params, func(string) {})

		return received["reasoning_effort"], err
	}

	// The configured one applies, the one chosen for the request wins over
	// it, and a server is sent none when nothing chooses one.
	for _, want := range []struct {
		effort, chosen string
		sent           any
	}{
		{"low", "", "low"},
		{"low", `{"effort":"high"}`, "high"},
		// An empty effort, as a front end may send for one run, is none
		// chosen.
		{"low", `{"effort":""}`, "low"},
		{"", "", nil},
	} {
		if sent, err := complete(want.effort, want.chosen); err != nil || sent != want.sent {
			t.Errorf("effort %q, chosen %q: sent %v, err %v; want %v", want.effort, want.chosen, sent, err, want.sent)
		}
	}

	if _, err := complete("", `{"temperature":1}`); err == nil || !strings.Contains(err.Error(), "invalid options") {
		t.Fatalf("an option the provider does not describe: err = %v", err)
	}
}

// Every field of the configuration, and every option, has a value the host
// can keep: what was given, or the provider's own default.
func TestInitializeSaysEveryDefault(t *testing.T) {
	said, err := testHandler(t).Initialize(context.Background(), protocol.InitializeParams{Config: json.RawMessage(`{"base_url":"http://localhost:11434/v1"}`)})

	if err != nil || string(said.Config) != `{"base_url":"http://localhost:11434/v1","api_key_env":"OPENAI_API_KEY","context_window":0,"effort":"","api":"chat"}` {
		t.Fatalf("config = %s, err = %v", said.Config, err)
	}

	for _, option := range said.Options {
		if len(option.Default) == 0 {
			t.Errorf("option %s has no default", option.Name)
		}
	}
}
