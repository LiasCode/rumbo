// Package openai is the built-in provider for servers that speak the OpenAI
// Chat Completions API, which covers OpenAI itself and most local and hosted
// model servers.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/LiasCode/rumbo/protocol"
)

// Config is what the provider accepts on initialize. The API key itself is
// never stored: APIKeyEnv names the environment variable that holds it.
type Config struct {
	BaseURL   string `json:"base_url"`
	APIKeyEnv string `json:"api_key_env"`
	// ContextWindow is the size of the model's context, in tokens. This API
	// does not report it, so it is said here when it is known, which matters
	// most for local models with small contexts.
	ContextWindow int64 `json:"context_window"`
}

// Handler implements protocol.Handler on Chat Completions.
type Handler struct {
	config Config
}

// New returns a handler that is ready after Initialize.
func New() *Handler {
	return &Handler{}
}

// Initialize implements protocol.Handler.
func (h *Handler) Initialize(_ context.Context, params protocol.InitializeParams) (protocol.InitializeResult, error) {
	h.config = Config{BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY"}

	if len(params.Config) > 0 {
		if err := json.Unmarshal(params.Config, &h.config); err != nil {
			return protocol.InitializeResult{}, fmt.Errorf("invalid config: %w", err)
		}
	}

	return protocol.InitializeResult{Protocol: protocol.Version, Name: "openai", Description: description}, nil
}

// description is what this provider lets the user choose and ask. It
// describes no options: which ones a server accepts depends on the server.
var description = protocol.Description{
	Model: &protocol.Option{Type: protocol.OptionString, Values: "models", Description: "Model to use"},
	Functions: []protocol.Function{{
		Name:        "models",
		Description: "List the models the server offers",
	}},
}

// Invoke implements protocol.Invoker.
func (h *Handler) Invoke(ctx context.Context, name string, _ json.RawMessage) (any, error) {
	if name != "models" {
		return nil, fmt.Errorf("unknown function %q", name)
	}

	response, err := h.send(ctx, http.MethodGet, "/models", nil)

	if err != nil {
		return nil, err
	}

	defer response.Body.Close()

	var listed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	if err := json.NewDecoder(response.Body).Decode(&listed); err != nil {
		return nil, fmt.Errorf("invalid model list: %w", err)
	}

	var result protocol.ValuesResult

	for _, model := range listed.Data {
		result.Values = append(result.Values, protocol.Value{Value: model.ID})
	}

	return result, nil
}

type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	Index    int    `json:"index,omitempty"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type chatRequest struct {
	Model         string        `json:"model"`
	Messages      []chatMessage `json:"messages"`
	Tools         []chatTool    `json:"tools,omitempty"`
	MaxTokens     int           `json:"max_completion_tokens,omitempty"`
	Stream        bool          `json:"stream"`
	StreamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content   string     `json:"content"`
			ToolCalls []toolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

// Complete implements protocol.Handler.
func (h *Handler) Complete(ctx context.Context, params protocol.CompleteParams, emit func(text string)) (protocol.CompleteResult, error) {
	body, err := json.Marshal(request(params))

	if err != nil {
		return protocol.CompleteResult{}, err
	}

	response, err := h.send(ctx, http.MethodPost, "/chat/completions", body)

	if err != nil {
		return protocol.CompleteResult{}, err
	}

	defer response.Body.Close()

	result, err := collect(response.Body, emit)
	result.ContextWindow = h.config.ContextWindow

	return result, err
}

// send makes one request to the server and returns its successful response,
// whose body the caller closes. Failures worth retrying are marked transient.
func (h *Handler) send(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	url := strings.TrimRight(h.config.BaseURL, "/") + path
	httpRequest, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))

	if err != nil {
		return nil, err
	}

	httpRequest.Header.Set("Content-Type", "application/json")

	if key := os.Getenv(h.config.APIKeyEnv); key != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+key)
	}

	response, err := http.DefaultClient.Do(httpRequest)

	// A request that never got an answer is worth sending again, unless it
	// was the host that cancelled it.
	if err != nil && ctx.Err() == nil {
		return nil, protocol.Transient(err)
	}

	if err != nil {
		return nil, err
	}

	if response.StatusCode == http.StatusOK {
		return response, nil
	}

	defer response.Body.Close()

	detail, _ := io.ReadAll(io.LimitReader(response.Body, 2000))
	failure := fmt.Errorf("%s: %s", response.Status, strings.TrimSpace(string(detail)))

	// Servers say a prompt does not fit in different words; these cover the
	// API itself and the common local ones.
	said := strings.ToLower(string(detail))

	switch status := response.StatusCode; {
	case status == http.StatusRequestTimeout, status == http.StatusTooManyRequests, status >= 500:
		return nil, protocol.Transient(failure)

	case strings.Contains(said, "context_length_exceeded"), strings.Contains(said, "context length"), strings.Contains(said, "context window"):
		return nil, protocol.Overflow(failure)
	}

	return nil, failure
}

func request(params protocol.CompleteParams) chatRequest {
	out := chatRequest{Model: params.Model, MaxTokens: params.MaxTokens, Stream: true}
	out.StreamOptions.IncludeUsage = true
	out.Messages = append(out.Messages, chatMessage{Role: "system", Content: params.System})

	for _, def := range params.Tools {
		tool := chatTool{Type: "function"}
		tool.Function.Name = def.Name
		tool.Function.Description = def.Description
		tool.Function.Parameters = def.Schema
		out.Tools = append(out.Tools, tool)
	}

	for _, message := range params.Messages {
		out.Messages = append(out.Messages, messages(message)...)
	}

	return out
}

// messages converts one protocol message. A user message can carry tool
// results and text together; this API wants each result as its own message,
// before the text.
func messages(message protocol.Message) []chatMessage {
	var results []chatMessage
	var texts []string
	var calls []toolCall

	for _, block := range message.Content {
		switch block.Type {
		case protocol.BlockText:
			texts = append(texts, block.Text)

		case protocol.BlockToolResult:
			results = append(results, chatMessage{Role: "tool", ToolCallID: block.ID, Content: block.Text})

		case protocol.BlockToolCall:
			call := toolCall{ID: block.ID, Type: "function"}
			call.Function.Name = block.Name
			call.Function.Arguments = string(block.Args)
			calls = append(calls, call)
		}
	}

	text := strings.Join(texts, "\n\n")

	if message.Role == protocol.RoleAssistant {
		return []chatMessage{{Role: "assistant", Content: text, ToolCalls: calls}}
	}

	if text != "" {
		results = append(results, chatMessage{Role: "user", Content: text})
	}

	return results
}

// collect reads the server-sent events of a streamed completion into the
// final message.
func collect(body io.Reader, emit func(text string)) (protocol.CompleteResult, error) {
	result := protocol.CompleteResult{Message: protocol.Message{Role: protocol.RoleAssistant}, Stop: protocol.StopEnd}

	var text strings.Builder
	var calls []*toolCall

	reader := bufio.NewReader(body)

	for {
		line, err := reader.ReadString('\n')
		data, found := strings.CutPrefix(strings.TrimSpace(line), "data:")
		data = strings.TrimSpace(data)

		if found && data != "" && data != "[DONE]" {
			var chunk chatChunk

			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return result, fmt.Errorf("invalid stream chunk: %w", err)
			}

			if chunk.Usage != nil {
				cached := chunk.Usage.PromptTokensDetails.CachedTokens
				result.Usage = protocol.Usage{
					Input:     chunk.Usage.PromptTokens - cached,
					Output:    chunk.Usage.CompletionTokens,
					CacheRead: cached,
				}
			}

			for _, choice := range chunk.Choices {
				if choice.Delta.Content != "" {
					text.WriteString(choice.Delta.Content)
					emit(choice.Delta.Content)
				}

				for _, part := range choice.Delta.ToolCalls {
					for len(calls) <= part.Index {
						calls = append(calls, &toolCall{})
					}

					call := calls[part.Index]
					call.ID += part.ID
					call.Function.Name += part.Function.Name
					call.Function.Arguments += part.Function.Arguments
				}

				if choice.FinishReason == "length" {
					result.Stop = protocol.StopMaxTokens
				}
			}
		}

		if err == io.EOF {
			break
		}

		if err != nil {
			return result, err
		}
	}

	if text.Len() > 0 {
		result.Message.Content = append(result.Message.Content, protocol.Block{Type: protocol.BlockText, Text: text.String()})
	}

	for _, call := range calls {
		args := call.Function.Arguments

		if args == "" {
			args = "{}"
		}

		// Arguments that are not JSON travel as a JSON string, so the host
		// can reject them and tell the model.
		if !json.Valid([]byte(args)) {
			quoted, _ := json.Marshal(args)
			args = string(quoted)
		}

		result.Message.Content = append(result.Message.Content, protocol.Block{
			Type: protocol.BlockToolCall, ID: call.ID, Name: call.Function.Name, Args: json.RawMessage(args),
		})
	}

	if len(calls) > 0 && result.Stop == protocol.StopEnd {
		result.Stop = protocol.StopToolCalls
	}

	return result, nil
}
