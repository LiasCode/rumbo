// Package openai is the built-in provider for servers that speak the OpenAI
// Chat Completions API, which covers OpenAI itself and most local and hosted
// model servers.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/LiasCode/rumbo/internal/providers/credentials"
	"github.com/LiasCode/rumbo/protocol"
)

// Config is what the provider accepts on initialize. The API key is never
// part of it: it comes from the environment variable APIKeyEnv names, or else
// from the key login stored, in the provider's own file.
type Config struct {
	BaseURL   string `json:"base_url"`
	APIKeyEnv string `json:"api_key_env"`
	// ContextWindow is the size of the model's context, in tokens. This API
	// does not report it, so it is said here when it is known, which matters
	// most for local models with small contexts.
	ContextWindow int64 `json:"context_window"`
	// Effort is the reasoning effort sent with every request, as the
	// server's model names it. Empty sends none.
	Effort string `json:"effort"`
	// API is the one spoken: chat, for Chat Completions, which most servers
	// offer, or responses, for OpenAI's Responses API. Empty is chat.
	API string `json:"api"`
}

// The APIs the provider speaks.
const (
	apiChat      = "chat"
	apiResponses = "responses"
)

// Handler implements protocol.Handler on Chat Completions.
type Handler struct {
	config Config
	// keys holds the API keys login stored, by server.
	keys credentials.Store
	// responses speaks the Responses API, with the key in force.
	responses *Responses
}

// New returns a handler that is ready after Initialize.
func New() *Handler {
	h := &Handler{keys: credentials.Default("openai")}

	h.responses = &Responses{Post: func(ctx context.Context, body []byte) (*http.Response, error) {
		return h.send(ctx, http.MethodPost, "/responses", body)
	}}

	return h
}

// server is the address of the server this provider reaches, which a stored
// key belongs to.
func (h *Handler) server() string {
	return strings.TrimRight(h.config.BaseURL, "/")
}

// key returns the API key for the next request: the environment's, else the
// one login stored. It is read for every request, so a login made in another
// process of this provider applies at once. A server that needs none gets
// none.
func (h *Handler) key() (string, error) {
	if key := os.Getenv(h.config.APIKeyEnv); key != "" {
		return key, nil
	}

	return h.keys.APIKey(h.server())
}

// Initialize implements protocol.Handler.
func (h *Handler) Initialize(_ context.Context, params protocol.InitializeParams) (protocol.InitializeResult, error) {
	h.config = Config{BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY"}

	if len(params.Config) > 0 {
		if err := json.Unmarshal(params.Config, &h.config); err != nil {
			return protocol.InitializeResult{}, fmt.Errorf("invalid config: %w", err)
		}
	}

	switch h.config.API {
	case "":
		h.config.API = apiChat
	case apiChat, apiResponses:
	default:
		return protocol.InitializeResult{}, fmt.Errorf("invalid config: api is %s or %s, not %q", apiChat, apiResponses, h.config.API)
	}

	// Every field is said, given or not, so the host can keep each with
	// its value.
	config, _ := json.Marshal(h.config)

	return protocol.InitializeResult{Protocol: protocol.Version, Name: "openai", Config: config, Description: description}, nil
}

// description is what this provider lets the user choose and ask. The
// values effort takes depend on the model, so they are not listed: the
// server refuses one its model does not know.
var description = protocol.Description{
	Model: &protocol.Option{Type: protocol.OptionString, Values: "models", Description: "Model to use"},
	Options: []protocol.Option{{
		Name:        "effort",
		Type:        protocol.OptionString,
		Description: "How much a reasoning model thinks, sent as reasoning_effort. The values depend on the model, such as minimal, low, medium, and high. Empty, none is sent and the model's own default applies.",
		Default:     json.RawMessage(`""`),
	}},
	Functions: []protocol.Function{
		{Name: "models", Description: "List the models the server offers"},
		{Name: "login", Description: "Store an API key for this server, once it is checked to work"},
		{
			Name:        "logout",
			Description: "Remove the API key login stored",
			Confirm:     "This removes the API key that login stored for this server. A key in the environment is not touched.",
		},
		{Name: "account", Description: "Say which key this provider uses, and where it comes from"},
	},
}

// InvokeDialog implements protocol.DialogInvoker.
func (h *Handler) InvokeDialog(ctx context.Context, name string, _ json.RawMessage, dialog protocol.Dialog) (any, error) {
	switch name {
	case "models":
		return h.models(ctx)
	case "login":
		return h.login(ctx, dialog)
	case "logout":
		return h.logout()
	case "account":
		return h.account()
	}

	return nil, fmt.Errorf("unknown function %q", name)
}

// login asks for an API key, checks it by listing the server's models, and
// stores it for this server.
func (h *Handler) login(ctx context.Context, dialog protocol.Dialog) (string, error) {
	answer, err := dialog.Ask(ctx, "API key for "+h.server()+":", true)

	if err != nil {
		return "", err
	}

	key := strings.TrimSpace(answer)

	if key == "" {
		return "", errors.New("no key was given; nothing changed")
	}

	dialog.Say("checking the key")

	response, err := h.sendWith(ctx, http.MethodGet, "/models", nil, key)

	if err != nil {
		return "", fmt.Errorf("the key was not stored: %w", err)
	}

	response.Body.Close()

	if err := h.keys.SetAPIKey(h.server(), key); err != nil {
		return "", err
	}

	text := fmt.Sprintf("logged in to %s; the key is kept in %s, which only you can read", h.server(), h.keys.Path)

	if os.Getenv(h.config.APIKeyEnv) != "" {
		text += fmt.Sprintf(". %s is set in the environment and still comes first", h.config.APIKeyEnv)
	}

	return text, nil
}

// logout forgets the key stored for this server.
func (h *Handler) logout() (string, error) {
	removed, err := h.keys.Remove(h.server())

	if err != nil {
		return "", err
	}

	if !removed {
		return "no key is stored for " + h.server(), nil
	}

	return "the key stored for " + h.server() + " is removed", nil
}

// account says where the key of the next request comes from, without
// showing it.
func (h *Handler) account() (string, error) {
	stored, err := h.keys.APIKey(h.server())

	if err != nil {
		return "", err
	}

	switch inEnvironment := os.Getenv(h.config.APIKeyEnv) != ""; {
	case inEnvironment && stored != "":
		return fmt.Sprintf("%s: using %s from the environment, which comes before the key login stored", h.server(), h.config.APIKeyEnv), nil
	case inEnvironment:
		return fmt.Sprintf("%s: using %s from the environment", h.server(), h.config.APIKeyEnv), nil
	case stored != "":
		return fmt.Sprintf("%s: using the key login stored, in %s", h.server(), h.keys.Path), nil
	}

	return fmt.Sprintf("%s: no key in %s or stored by login; requests go without one, which many local servers accept", h.server(), h.config.APIKeyEnv), nil
}

func (h *Handler) models(ctx context.Context) (protocol.ValuesResult, error) {
	var result protocol.ValuesResult

	response, err := h.send(ctx, http.MethodGet, "/models", nil)

	if err != nil {
		return result, err
	}

	defer response.Body.Close()

	var listed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	if err := json.NewDecoder(response.Body).Decode(&listed); err != nil {
		return result, fmt.Errorf("invalid model list: %w", err)
	}

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
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	Tools     []chatTool    `json:"tools,omitempty"`
	MaxTokens int           `json:"max_completion_tokens,omitempty"`
	// ReasoningEffort is left out when none is chosen, so a server that
	// does not know it is not sent it.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	Stream          bool   `json:"stream"`
	StreamOptions   struct {
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
	chosen, err := h.chosen(params.Options)

	if err != nil {
		return protocol.CompleteResult{}, err
	}

	if h.config.API == apiResponses {
		return h.respond(ctx, params, chosen, emit)
	}

	out := request(params)
	out.ReasoningEffort = chosen.Effort

	body, err := json.Marshal(out)

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

// send makes a request with the key in force.
func (h *Handler) send(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	key, err := h.key()

	if err != nil {
		return nil, err
	}

	return h.sendWith(ctx, method, path, body, key)
}

// sendWith makes one request to the server, with key or without one when it
// is empty, and returns its successful response, whose body the caller
// closes. Failures worth retrying are marked transient.
func (h *Handler) sendWith(ctx context.Context, method, path string, body []byte, key string) (*http.Response, error) {
	url := h.server() + path
	httpRequest, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))

	if err != nil {
		return nil, err
	}

	httpRequest.Header.Set("Content-Type", "application/json")

	if key != "" {
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

// options are the values a request may carry, as described above.
type options struct {
	Effort string `json:"effort"`
}

// chosen returns the options a request carries, over those of the
// configuration. One the provider does not describe is an error.
func (h *Handler) chosen(raw json.RawMessage) (options, error) {
	var chosen options

	if len(raw) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()

		if err := decoder.Decode(&chosen); err != nil {
			return chosen, fmt.Errorf("invalid options: %w", err)
		}
	}

	// An empty effort is none chosen: the host sends the option's default,
	// "", with every request, which would otherwise hide the configured one.
	if chosen.Effort == "" {
		chosen.Effort = h.config.Effort
	}

	return chosen, nil
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
		result.Message.Content = append(result.Message.Content, protocol.Block{
			Type: protocol.BlockToolCall, ID: call.ID, Name: call.Function.Name, Args: arguments(call.Function.Arguments),
		})
	}

	if len(calls) > 0 && result.Stop == protocol.StopEnd {
		result.Stop = protocol.StopToolCalls
	}

	return result, nil
}

// arguments are the arguments of a tool call as the model wrote them. Ones
// that are not JSON travel as a JSON string, so the host can reject them and
// tell the model.
func arguments(written string) json.RawMessage {
	if written == "" {
		return json.RawMessage("{}")
	}

	if !json.Valid([]byte(written)) {
		quoted, _ := json.Marshal(written)

		return quoted
	}

	return json.RawMessage(written)
}
