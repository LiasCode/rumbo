package openai

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/LiasCode/rumbo/protocol"
)

// This file speaks OpenAI's Responses API, which some reasoning models need
// in order to use tools with a reasoning effort. Every request carries the
// whole conversation and nothing is stored on the server, as the protocol
// asks of a provider. The reasoning a model wrote travels back to it as an
// opaque block, encrypted.

// Responses completes a conversation with the Responses API. It is shared
// by the providers that speak it, which give what differs between them: how
// a request reaches the server with its credentials, what a failure the
// stream reports means, and whether the server takes a limit on the output.
type Responses struct {
	// Post sends a request to the responses endpoint and returns the
	// successful response, whose body streams the answer. key is the
	// request's prompt_cache_key, for a server that takes it from elsewhere
	// than the body.
	Post func(ctx context.Context, body []byte, key string) (*http.Response, error)
	// Failure is what a failure the stream reports means. Without one, it
	// means what StreamFailure says.
	Failure func(code, message string) error
	// Unlimited leaves out the limit on the output, for a server that
	// refuses one.
	Unlimited bool

	mu sync.Mutex
	// unreasoning holds the models that said they have no reasoning to give
	// back, so they are not asked for it again.
	unreasoning map[string]bool
}

// keptReasoning is the type of the opaque data that holds a reasoning item.
// It differs from the types other providers keep, so they skip it.
const keptReasoning = "openai.reasoning"

// kept is the opaque data of a reasoning item: the item as the server gave
// it, and the model that wrote it, which is the only one that can read it.
type kept struct {
	Type  string          `json:"type"`
	Model string          `json:"model"`
	Item  json.RawMessage `json:"item"`
}

// encryptedReasoning asks the server for the reasoning of a model in a form
// that can be sent back to it on later requests.
const encryptedReasoning = "reasoning.encrypted_content"

type responsesRequest struct {
	Model           string          `json:"model"`
	Instructions    string          `json:"instructions,omitempty"`
	Input           []any           `json:"input"`
	Tools           []responsesTool `json:"tools,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
	// Reasoning is left out when no effort is chosen.
	Reasoning *responsesReasoning `json:"reasoning,omitempty"`
	Store     bool                `json:"store"`
	// PromptCacheKey sends every request of a conversation to the servers
	// that hold its cached prefix. Without it the server picks one by the
	// first tokens alone, and requests a few seconds apart can miss the
	// cache the previous one filled.
	PromptCacheKey string   `json:"prompt_cache_key,omitempty"`
	Include        []string `json:"include,omitempty"`
	Stream         bool     `json:"stream"`
}

type responsesReasoning struct {
	Effort string `json:"effort"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	// Strict is false: the tools' schemas have optional fields, which a
	// strict schema cannot.
	Strict bool `json:"strict"`
}

// inputItem is a message, a function call, or a function call's output.
type inputItem struct {
	Type string `json:"type"`
	Role string `json:"role,omitempty"`
	// Content is the text of a message, or a list of its parts when it
	// shows images.
	Content   any    `json:"content,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	// Output is the text of a tool's result, or a list of its parts when it
	// shows images.
	Output any `json:"output,omitempty"`
}

// inputPart is one part of a message, or of a tool's result, that shows
// images.
type inputPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

// toolOutput is what a tool's result sends: its text alone, or its text
// and then the images it shows, each as a data URL.
func toolOutput(block protocol.Block) any {
	if len(block.Images) == 0 {
		return block.Text
	}

	parts := []inputPart{{Type: "input_text", Text: block.Text}}

	for _, image := range block.Images {
		parts = append(parts, inputPart{Type: "input_image", ImageURL: dataURL(image)})
	}

	return parts
}

// messageContent is what a message's text sends: the text alone, or the
// images a person gave and then their words, when there are any.
func messageContent(block protocol.Block) any {
	if len(block.Images) == 0 {
		return block.Text
	}

	var parts []inputPart

	for _, image := range block.Images {
		parts = append(parts, inputPart{Type: "input_image", ImageURL: dataURL(image)})
	}

	if block.Text != "" {
		parts = append(parts, inputPart{Type: "input_text", Text: block.Text})
	}

	return parts
}

// respond completes params with the Responses API.
func (h *Handler) respond(ctx context.Context, params protocol.CompleteParams, chosen options, emit func(text string)) (protocol.CompleteResult, error) {
	result, err := h.responses.Complete(ctx, params, chosen.Effort, emit)
	result.ContextWindow = h.config.ContextWindow

	return result, err
}

// Complete completes params, with the reasoning effort given, or the
// model's own when it is empty.
func (r *Responses) Complete(ctx context.Context, params protocol.CompleteParams, effort string, emit func(text string)) (protocol.CompleteResult, error) {
	out := responsesFor(params, effort)

	if r.Unlimited {
		out.MaxOutputTokens = 0
	}

	r.mu.Lock()
	encrypted := !r.unreasoning[params.Model]
	r.mu.Unlock()

	if encrypted {
		out.Include = []string{encryptedReasoning}
	}

	response, err := r.post(ctx, out)

	// A model that does not reason has no encrypted reasoning to give and
	// refuses the request that asks for it. It is asked again without, and
	// not asked for it again.
	if err != nil && encrypted && strings.Contains(strings.ToLower(err.Error()), "encrypted content is not supported") {
		r.mu.Lock()

		if r.unreasoning == nil {
			r.unreasoning = map[string]bool{}
		}

		r.unreasoning[params.Model] = true
		r.mu.Unlock()

		out.Include = nil
		response, err = r.post(ctx, out)
	}

	if err != nil {
		return protocol.CompleteResult{}, err
	}

	defer response.Body.Close()

	failure := r.Failure

	if failure == nil {
		failure = StreamFailure
	}

	return collectResponse(response.Body, params.Model, emit, failure)
}

// post sends a request as JSON.
func (r *Responses) post(ctx context.Context, body responsesRequest) (*http.Response, error) {
	raw, err := json.Marshal(body)

	if err != nil {
		return nil, err
	}

	return r.Post(ctx, raw, body.PromptCacheKey)
}

// responsesFor converts params into a request of the Responses API.
func responsesFor(params protocol.CompleteParams, effort string) responsesRequest {
	out := responsesRequest{
		Model:           params.Model,
		Instructions:    params.System,
		Input:           []any{},
		MaxOutputTokens: params.MaxTokens,
		PromptCacheKey:  cacheKey(params),
		Stream:          true,
	}

	if effort != "" {
		out.Reasoning = &responsesReasoning{Effort: effort}
	}

	for _, def := range params.Tools {
		out.Tools = append(out.Tools, responsesTool{Type: "function", Name: def.Name, Description: def.Description, Parameters: def.Schema})
	}

	for _, message := range params.Messages {
		out.Input = append(out.Input, inputItems(message, params.Model)...)
	}

	return out
}

// cacheKey names the conversation params belong to, for the server's cache.
// It is made of what starts the conversation, the instructions, the tools,
// and the first message, which every later request of it repeats unchanged:
// the history only grows by appending. A compaction starts the conversation
// over, and with it the key, as it does the prefix the cache holds. The
// same start makes the same key, so a branch keeps its parent's cache.
func cacheKey(params protocol.CompleteParams) string {
	if len(params.Messages) == 0 {
		return ""
	}

	start, _ := json.Marshal(struct {
		System string             `json:"system"`
		Tools  []protocol.ToolDef `json:"tools"`
		First  protocol.Message   `json:"first"`
	}{params.System, params.Tools, params.Messages[0]})

	sum := sha256.Sum256(start)

	return hex.EncodeToString(sum[:16])
}

// inputItems converts one protocol message into items of the input, in the
// order of its blocks.
func inputItems(message protocol.Message, model string) []any {
	var items []any

	for index, block := range message.Content {
		switch block.Type {
		case protocol.BlockText:
			items = append(items, inputItem{Type: "message", Role: message.Role, Content: messageContent(block)})

		case protocol.BlockToolCall:
			items = append(items, inputItem{Type: "function_call", CallID: block.ID, Name: block.Name, Arguments: string(block.Args)})

		case protocol.BlockToolResult:
			items = append(items, inputItem{Type: "function_call_output", CallID: block.ID, Output: toolOutput(block)})

		case protocol.BlockOpaque:
			if item, ok := replayable(message.Content, index, model); ok {
				items = append(items, item)
			}
		}
	}

	return items
}

// replayable returns the reasoning item in the opaque block at index, when
// it can be sent back: it was written by model, which is the only one that
// can read it, and the item it led to follows it, which the server requires.
func replayable(blocks []protocol.Block, index int, model string) (json.RawMessage, bool) {
	var reasoning kept

	if json.Unmarshal(blocks[index].Data, &reasoning) != nil || reasoning.Type != keptReasoning || reasoning.Model != model {
		return nil, false
	}

	for _, later := range blocks[index+1:] {
		if later.Type == protocol.BlockText || later.Type == protocol.BlockToolCall {
			return reasoning.Item, true
		}
	}

	return nil, false
}

// responseEvent is one server-sent event of a streamed response. Each event
// has only the fields of its type.
type responseEvent struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta"`
	Response *responseObject `json:"response"`
	Code     string          `json:"code"`
	Message  string          `json:"message"`
}

type responseObject struct {
	Status            string            `json:"status"`
	Output            []json.RawMessage `json:"output"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Usage struct {
		InputTokens        int64 `json:"input_tokens"`
		OutputTokens       int64 `json:"output_tokens"`
		InputTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
}

type outputItem struct {
	Type    string `json:"type"`
	Content []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// collectResponse reads a streamed response. The visible text is emitted as
// it comes; the message is built from the response the stream ends with,
// which holds every item whole.
func collectResponse(body io.Reader, model string, emit func(text string), failure func(code, message string) error) (protocol.CompleteResult, error) {
	result := protocol.CompleteResult{Message: protocol.Message{Role: protocol.RoleAssistant}, Stop: protocol.StopEnd}

	var final *responseObject

	reader := bufio.NewReader(body)

	for final == nil {
		line, err := reader.ReadString('\n')
		data, found := strings.CutPrefix(strings.TrimSpace(line), "data:")
		data = strings.TrimSpace(data)

		if found && data != "" {
			var event responseEvent

			if err := json.Unmarshal([]byte(data), &event); err != nil {
				return result, fmt.Errorf("invalid stream event: %w", err)
			}

			switch event.Type {
			case "response.output_text.delta":
				emit(event.Delta)

			case "response.completed", "response.incomplete":
				final = event.Response

			case "response.failed":
				if event.Response != nil && event.Response.Error != nil {
					return result, failure(event.Response.Error.Code, event.Response.Error.Message)
				}

				return result, errors.New("the response failed")

			case "error":
				return result, failure(event.Code, event.Message)
			}
		}

		// A stream that ends before its response is a dropped connection,
		// worth sending again.
		if err == io.EOF && final == nil {
			return result, protocol.Transient(errors.New("the response stream ended before the response did"))
		}

		if err != nil && err != io.EOF {
			return result, err
		}
	}

	cached := final.Usage.InputTokensDetails.CachedTokens
	result.Usage = protocol.Usage{Input: final.Usage.InputTokens - cached, Output: final.Usage.OutputTokens, CacheRead: cached}

	refused, called := false, false

	for _, raw := range final.Output {
		var item outputItem

		if err := json.Unmarshal(raw, &item); err != nil {
			return result, fmt.Errorf("invalid output item: %w", err)
		}

		switch item.Type {
		case "reasoning":
			data, _ := json.Marshal(kept{Type: keptReasoning, Model: model, Item: raw})
			result.Message.Content = append(result.Message.Content, protocol.Block{Type: protocol.BlockOpaque, Data: data})

		case "message":
			for _, part := range item.Content {
				text := part.Text

				if part.Type == "refusal" {
					text, refused = part.Refusal, true
				}

				if text != "" {
					result.Message.Content = append(result.Message.Content, protocol.Block{Type: protocol.BlockText, Text: text})
				}
			}

		case "function_call":
			called = true
			result.Message.Content = append(result.Message.Content, protocol.Block{
				Type: protocol.BlockToolCall, ID: item.CallID, Name: item.Name, Args: arguments(item.Arguments),
			})
		}
	}

	incomplete := ""

	if final.IncompleteDetails != nil {
		incomplete = final.IncompleteDetails.Reason
	}

	switch {
	case incomplete == "max_output_tokens":
		result.Stop = protocol.StopMaxTokens
	case refused, incomplete == "content_filter":
		result.Stop = protocol.StopRefusal
	case called:
		result.Stop = protocol.StopToolCalls
	}

	return result, nil
}

// StreamFailure is a failure the server reported in the stream, marked so
// the host knows what to do with it.
func StreamFailure(code, message string) error {
	failure := fmt.Errorf("%s: %s", code, message)

	switch code {
	case "rate_limit_exceeded", "server_error", "server_is_overloaded":
		return protocol.Transient(failure)
	case "context_length_exceeded":
		return protocol.Overflow(failure)
	}

	return failure
}
