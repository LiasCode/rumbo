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

	"rumbo/protocol"
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

	handler := New()
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
