package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/LiasCode/rumbo/protocol"
)

// responsesHandler is a handler that speaks the Responses API to server.
func responsesHandler(t *testing.T, server string) *Handler {
	t.Helper()

	handler := testHandler(t)
	config, _ := json.Marshal(Config{BaseURL: server, APIKeyEnv: "RUMBO_TEST_NO_KEY", API: apiResponses})

	if _, err := handler.Initialize(context.Background(), protocol.InitializeParams{Config: config}); err != nil {
		t.Fatal(err)
	}

	return handler
}

// stream writes events as the server sends them.
func stream(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")

	for _, event := range events {
		var header struct {
			Type string `json:"type"`
		}

		json.Unmarshal([]byte(event), &header)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", header.Type, event)
	}
}

func reasoningBlock(model, id string) protocol.Block {
	data, _ := json.Marshal(kept{Type: keptReasoning, Model: model, Item: json.RawMessage(`{"type":"reasoning","id":"` + id + `","summary":[],"encrypted_content":"enc-` + id + `"}`)})

	return protocol.Block{Type: protocol.BlockOpaque, Data: data}
}

func TestResponsesCarryTheWholeConversation(t *testing.T) {
	var received map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}

		json.NewDecoder(r.Body).Decode(&received)

		stream(w,
			`{"type":"response.created","response":{"status":"in_progress","output":[]}}`,
			`{"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"Looking."}`,
			`{"type":"response.completed","response":{"status":"completed","output":[`+
				`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"enc-1"},`+
				`{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Looking.","annotations":[]}]},`+
				`{"type":"function_call","id":"fc_1","call_id":"call_2","name":"bash","arguments":"{\"command\":\"ls\"}","status":"completed"}],`+
				`"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":60},"output_tokens":9,"total_tokens":109}}}`,
		)
	}))

	defer server.Close()

	handler := responsesHandler(t, server.URL)

	var streamed strings.Builder

	result, err := handler.Complete(context.Background(), protocol.CompleteParams{
		Model:   "reasoner",
		System:  "be brief",
		Tools:   []protocol.ToolDef{{Name: "bash", Description: "Run a command", Schema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`)}},
		Options: json.RawMessage(`{"effort":"high"}`),
		Messages: []protocol.Message{
			{Role: protocol.RoleUser, Content: []protocol.Block{{Type: protocol.BlockText, Text: "list files"}}},
			// Of the reasoning kept, only what this model wrote and what led
			// to something goes back.
			{Role: protocol.RoleAssistant, Content: []protocol.Block{
				reasoningBlock("reasoner", "rs_0"),
				reasoningBlock("another", "rs_x"),
				{Type: protocol.BlockToolCall, ID: "c0", Name: "bash", Args: json.RawMessage(`{"command":"pwd"}`)},
				reasoningBlock("reasoner", "rs_cut"),
			}},
			{Role: protocol.RoleUser, Content: []protocol.Block{
				{Type: protocol.BlockToolResult, ID: "c0", Text: "/tmp"},
				{Type: protocol.BlockText, Text: "and now?"},
			}},
		},
	}, func(text string) { streamed.WriteString(text) })

	if err != nil {
		t.Fatal(err)
	}

	var want map[string]any

	json.Unmarshal([]byte(`{
		"model": "reasoner",
		"instructions": "be brief",
		"input": [
			{"type": "message", "role": "user", "content": "list files"},
			{"type": "reasoning", "id": "rs_0", "summary": [], "encrypted_content": "enc-rs_0"},
			{"type": "function_call", "call_id": "c0", "name": "bash", "arguments": "{\"command\":\"pwd\"}"},
			{"type": "function_call_output", "call_id": "c0", "output": "/tmp"},
			{"type": "message", "role": "user", "content": "and now?"}
		],
		"tools": [{"type": "function", "name": "bash", "description": "Run a command", "parameters": {"type": "object", "properties": {"command": {"type": "string"}}}, "strict": false}],
		"reasoning": {"effort": "high"},
		"store": false,
		"include": ["reasoning.encrypted_content"],
		"stream": true
	}`), &want)

	// The cache key is checked on its own, below.
	if key, _ := received["prompt_cache_key"].(string); len(key) != 32 {
		t.Fatalf("prompt_cache_key = %v", received["prompt_cache_key"])
	}

	delete(received, "prompt_cache_key")

	if !reflect.DeepEqual(received, want) {
		sent, _ := json.MarshalIndent(received, "", "  ")
		t.Fatalf("request:\n%s", sent)
	}

	if streamed.String() != "Looking." {
		t.Fatalf("streamed %q", streamed.String())
	}

	if result.Stop != protocol.StopToolCalls || result.Usage != (protocol.Usage{Input: 40, Output: 9, CacheRead: 60}) || len(result.Message.Content) != 3 {
		t.Fatalf("result = %+v", result)
	}

	var reasoning kept

	json.Unmarshal(result.Message.Content[0].Data, &reasoning)

	if reasoning.Type != keptReasoning || reasoning.Model != "reasoner" || !strings.Contains(string(reasoning.Item), `"encrypted_content":"enc-1"`) {
		t.Fatalf("kept reasoning = %+v", reasoning)
	}

	if text, call := result.Message.Content[1], result.Message.Content[2]; text.Text != "Looking." || call.ID != "call_2" || call.Name != "bash" || string(call.Args) != `{"command":"ls"}` {
		t.Fatalf("message = %+v", result.Message.Content)
	}
}

// Every request of a conversation names it with the same cache key, so the
// server sends it where its prefix is cached; another conversation, or the
// same one started over by a compaction, has another.
func TestAConversationKeepsItsCacheKey(t *testing.T) {
	var keys []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Key string `json:"prompt_cache_key"`
		}

		json.NewDecoder(r.Body).Decode(&body)
		keys = append(keys, body.Key)

		stream(w, `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}}`)
	}))

	defer server.Close()

	handler := responsesHandler(t, server.URL)
	tools := []protocol.ToolDef{{Name: "bash", Description: "Run a command", Schema: json.RawMessage(`{"type":"object"}`)}}
	user := func(text string) protocol.Message {
		return protocol.Message{Role: protocol.RoleUser, Content: []protocol.Block{{Type: protocol.BlockText, Text: text}}}
	}
	call := protocol.Message{Role: protocol.RoleAssistant, Content: []protocol.Block{{Type: protocol.BlockToolCall, ID: "c1", Name: "bash", Args: json.RawMessage(`{}`)}}}
	result := protocol.Message{Role: protocol.RoleUser, Content: []protocol.Block{{Type: protocol.BlockToolResult, ID: "c1", Text: "done"}}}

	conversations := [][]protocol.Message{
		{user("context and task")},
		{user("context and task"), call, result},
		{user("context and task"), call, result, call, result},
		{user("another task")},
		{user("context and summary")},
	}

	for _, messages := range conversations {
		if _, err := handler.Complete(context.Background(), protocol.CompleteParams{Model: "m", System: "be brief", Tools: tools, Messages: messages}, func(string) {}); err != nil {
			t.Fatal(err)
		}
	}

	if keys[0] == "" || keys[1] != keys[0] || keys[2] != keys[0] {
		t.Fatalf("one conversation had keys %q", keys[:3])
	}

	if keys[3] == keys[0] || keys[4] == keys[0] || keys[4] == keys[3] {
		t.Fatalf("different conversations share a key: %q", keys)
	}
}

// A model that does not reason refuses a request that asks for its
// reasoning; it is asked again without, and not asked for it again.
func TestResponsesForAModelThatDoesNotReason(t *testing.T) {
	var asked []bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		_, include := body["include"]
		asked = append(asked, include)

		if include {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"Encrypted content is not supported with this model.","type":"invalid_request_error"}}`)

			return
		}

		stream(w, `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":3,"output_tokens":1}}}`)
	}))

	defer server.Close()

	handler := responsesHandler(t, server.URL)
	params := protocol.CompleteParams{Model: "plain", Messages: []protocol.Message{{Role: protocol.RoleUser, Content: []protocol.Block{{Type: protocol.BlockText, Text: "hi"}}}}}

	for range 2 {
		if result, err := handler.Complete(context.Background(), params, func(string) {}); err != nil || result.Stop != protocol.StopEnd {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
	}

	if !reflect.DeepEqual(asked, []bool{true, false, false}) {
		t.Fatalf("include sent on the requests: %v", asked)
	}
}

func TestResponsesSayHowTheyEnded(t *testing.T) {
	for name, want := range map[string]struct {
		events []string
		stop   string
		check  func(error) bool
	}{
		"cut by the output limit": {
			events: []string{`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"half"}]}],"usage":{}}}`},
			stop:   protocol.StopMaxTokens,
		},
		"refused": {
			events: []string{`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"I cannot help with that."}]}],"usage":{}}}`},
			stop:   protocol.StopRefusal,
		},
		"too long": {
			events: []string{`{"type":"response.failed","response":{"status":"failed","error":{"code":"context_length_exceeded","message":"Your input exceeds the context window."},"output":[]}}`},
			check:  protocol.IsOverflow,
		},
		"rate limited": {
			events: []string{`{"type":"error","code":"rate_limit_exceeded","message":"Slow down."}`},
			check:  protocol.IsTransient,
		},
		"dropped": {
			events: []string{`{"type":"response.output_text.delta","delta":"par"}`},
			check:  protocol.IsTransient,
		},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stream(w, want.events...)
		}))

		handler := responsesHandler(t, server.URL)
		params := protocol.CompleteParams{Model: "reasoner", Messages: []protocol.Message{{Role: protocol.RoleUser, Content: []protocol.Block{{Type: protocol.BlockText, Text: "hi"}}}}}
		result, err := handler.Complete(context.Background(), params, func(string) {})

		server.Close()

		switch {
		case want.check != nil && (err == nil || !want.check(err)):
			t.Errorf("%s: err = %v", name, err)
		case want.check == nil && (err != nil || result.Stop != want.stop):
			t.Errorf("%s: stop = %q, err = %v; want %q", name, result.Stop, err, want.stop)
		}
	}
}

func TestAnUnknownAPIIsRefused(t *testing.T) {
	config, _ := json.Marshal(Config{API: "completions"})

	if _, err := testHandler(t).Initialize(context.Background(), protocol.InitializeParams{Config: config}); err == nil || !strings.Contains(err.Error(), "api is chat or responses") {
		t.Fatalf("err = %v", err)
	}
}
