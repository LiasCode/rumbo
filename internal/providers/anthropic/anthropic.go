// Package anthropic is the built-in provider for the Claude API. It is an
// ordinary protocol.Handler: the host reaches it through the same protocol as
// any provider a user writes.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"rumbo/protocol"
)

// Streaming removes the timeout concern, so the default leaves room to think
// and to write whole files.
const defaultMaxTokens = 64_000

// Config is what the provider accepts on initialize. Credentials are not
// part of it: the SDK reads them from the environment (ANTHROPIC_API_KEY,
// ANTHROPIC_AUTH_TOKEN) or from the profile created by "ant auth login".
type Config struct {
	BaseURL string `json:"base_url"`
	// Effort is one of low, medium, high, xhigh, max. Empty uses the model's
	// default.
	Effort string `json:"effort"`
}

// Handler implements protocol.Handler on the Messages API.
type Handler struct {
	client sdk.Client
	config Config
}

// New returns a handler that is ready after Initialize.
func New() *Handler {
	return &Handler{}
}

// Initialize implements protocol.Handler.
func (h *Handler) Initialize(_ context.Context, params protocol.InitializeParams) (protocol.InitializeResult, error) {
	if len(params.Config) > 0 {
		if err := json.Unmarshal(params.Config, &h.config); err != nil {
			return protocol.InitializeResult{}, fmt.Errorf("invalid config: %w", err)
		}
	}

	var options []option.RequestOption

	if h.config.BaseURL != "" {
		options = append(options, option.WithBaseURL(h.config.BaseURL))
	}

	h.client = sdk.NewClient(options...)

	return protocol.InitializeResult{Protocol: protocol.Version, Name: "anthropic", Description: description}, nil
}

// description is what this provider lets the user choose and ask.
var description = protocol.Description{
	Model: &protocol.Option{Type: protocol.OptionString, Values: "models", Description: "Claude model to use"},
	Options: []protocol.Option{{
		Name:        "effort",
		Type:        protocol.OptionString,
		Enum:        []string{"low", "medium", "high", "xhigh", "max"},
		Description: "How much the model thinks and how thorough it is. Without it, the model's own default applies.",
	}},
	Functions: []protocol.Function{{
		Name:        "models",
		Description: "List the models this account can use",
	}},
}

// options are the values a request may carry, as described above.
type options struct {
	Effort string `json:"effort"`
}

// Invoke implements protocol.Invoker.
func (h *Handler) Invoke(ctx context.Context, name string, _ json.RawMessage) (any, error) {
	if name != "models" {
		return nil, fmt.Errorf("unknown function %q", name)
	}

	var result protocol.ValuesResult

	pages := h.client.Models.ListAutoPaging(ctx, sdk.ModelListParams{})

	for pages.Next() {
		model := pages.Current()
		result.Values = append(result.Values, protocol.Value{Value: model.ID, Label: model.DisplayName})
	}

	return result, pages.Err()
}

// Complete implements protocol.Handler.
func (h *Handler) Complete(ctx context.Context, params protocol.CompleteParams, emit func(text string)) (protocol.CompleteResult, error) {
	request, err := h.request(params)

	if err != nil {
		return protocol.CompleteResult{}, err
	}

	stream := h.client.Messages.NewStreaming(ctx, request)
	message := sdk.Message{}

	for stream.Next() {
		event := stream.Current()

		if err := message.Accumulate(event); err != nil {
			return protocol.CompleteResult{}, err
		}

		if delta, ok := event.AsAny().(sdk.ContentBlockDeltaEvent); ok {
			if text, ok := delta.Delta.AsAny().(sdk.TextDelta); ok {
				emit(text.Text)
			}
		}
	}

	if err := stream.Err(); err != nil {
		return protocol.CompleteResult{}, classify(ctx, err)
	}

	return result(message), nil
}

// classify marks the failures worth retrying: the statuses the API documents
// as temporary, and connections that failed or dropped. The SDK has already
// retried these briefly; the host waits longer.
func classify(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return err
	}

	var failure *sdk.Error

	if errors.As(err, &failure) {
		switch status := failure.StatusCode; {
		case status == 408, status == 409, status == 429, status >= 500:
			return protocol.Transient(err)
		}

		return err
	}

	var network net.Error

	if errors.As(err, &network) || errors.Is(err, io.ErrUnexpectedEOF) {
		return protocol.Transient(err)
	}

	return err
}

func (h *Handler) request(params protocol.CompleteParams) (sdk.MessageNewParams, error) {
	request := sdk.MessageNewParams{
		Model:     params.Model,
		MaxTokens: int64(params.MaxTokens),
		System:    []sdk.TextBlockParam{{Text: params.System}},
		// The host only ever appends to the history, so caching up to the last
		// block makes every request reuse the previous one.
		CacheControl: sdk.NewCacheControlEphemeralParam(),
	}

	if params.MaxTokens == 0 {
		request.MaxTokens = defaultMaxTokens
	}

	// An option chosen for the request wins over the provider's configuration.
	chosen := options{Effort: h.config.Effort}

	if len(params.Options) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(params.Options))
		decoder.DisallowUnknownFields()

		if err := decoder.Decode(&chosen); err != nil {
			return request, fmt.Errorf("invalid options: %w", err)
		}
	}

	if chosen.Effort != "" {
		request.OutputConfig = sdk.OutputConfigParam{Effort: sdk.OutputConfigEffort(chosen.Effort)}
	}

	for _, def := range params.Tools {
		var schema struct {
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		}

		if err := json.Unmarshal(def.Schema, &schema); err != nil {
			return request, fmt.Errorf("tool %s: invalid schema: %w", def.Name, err)
		}

		request.Tools = append(request.Tools, sdk.ToolUnionParam{OfTool: &sdk.ToolParam{
			Name:        def.Name,
			Description: sdk.String(def.Description),
			InputSchema: sdk.ToolInputSchemaParam{Properties: schema.Properties, Required: schema.Required},
			// Large arguments, such as file contents, stream as they are
			// generated. The host validates the arguments before running a tool.
			EagerInputStreaming: sdk.Bool(true),
		}})
	}

	for _, message := range params.Messages {
		blocks, err := blocks(message)

		if err != nil {
			return request, err
		}

		if message.Role == protocol.RoleAssistant {
			request.Messages = append(request.Messages, sdk.NewAssistantMessage(blocks...))
		} else {
			request.Messages = append(request.Messages, sdk.NewUserMessage(blocks...))
		}
	}

	return request, nil
}

// reasoning is the opaque data kept for a thinking block, which the API
// requires back unchanged on later requests.
type reasoning struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`
}

func blocks(message protocol.Message) ([]sdk.ContentBlockParamUnion, error) {
	var out []sdk.ContentBlockParamUnion

	for _, block := range message.Content {
		switch block.Type {
		case protocol.BlockText:
			out = append(out, sdk.NewTextBlock(block.Text))

		case protocol.BlockToolCall:
			// The API only accepts an object here, whatever the model sent.
			input := map[string]any{}
			_ = json.Unmarshal(block.Args, &input)

			out = append(out, sdk.NewToolUseBlock(block.ID, input, block.Name))

		case protocol.BlockToolResult:
			out = append(out, sdk.NewToolResultBlock(block.ID, block.Text, block.IsError))

		case protocol.BlockOpaque:
			// Opaque data left by another provider is not ours to read; it
			// decodes to no known type and is skipped.
			var kept reasoning
			_ = json.Unmarshal(block.Data, &kept)

			switch kept.Type {
			case "thinking":
				out = append(out, sdk.NewThinkingBlock(kept.Signature, kept.Thinking))
			case "redacted_thinking":
				out = append(out, sdk.NewRedactedThinkingBlock(kept.Data))
			}
		}
	}

	return out, nil
}

func result(message sdk.Message) protocol.CompleteResult {
	out := protocol.CompleteResult{
		Message: protocol.Message{Role: protocol.RoleAssistant},
		Stop:    stop(message.StopReason),
		Usage: protocol.Usage{
			Input:      message.Usage.InputTokens,
			Output:     message.Usage.OutputTokens,
			CacheRead:  message.Usage.CacheReadInputTokens,
			CacheWrite: message.Usage.CacheCreationInputTokens,
		},
	}

	for _, block := range message.Content {
		switch variant := block.AsAny().(type) {
		case sdk.TextBlock:
			out.Message.Content = append(out.Message.Content, protocol.Block{Type: protocol.BlockText, Text: variant.Text})

		case sdk.ToolUseBlock:
			args := variant.Input

			// Eagerly streamed arguments are not validated by the API. Invalid
			// ones travel as a JSON string, so the host can reject them.
			if !json.Valid(args) {
				args, _ = json.Marshal(string(args))
			}

			out.Message.Content = append(out.Message.Content, protocol.Block{
				Type: protocol.BlockToolCall, ID: variant.ID, Name: variant.Name, Args: args,
			})

		case sdk.ThinkingBlock:
			out.Message.Content = append(out.Message.Content, opaque(reasoning{
				Type: "thinking", Thinking: variant.Thinking, Signature: variant.Signature,
			}))

		case sdk.RedactedThinkingBlock:
			out.Message.Content = append(out.Message.Content, opaque(reasoning{Type: "redacted_thinking", Data: variant.Data}))
		}
	}

	return out
}

func opaque(kept reasoning) protocol.Block {
	raw, _ := json.Marshal(kept)

	return protocol.Block{Type: protocol.BlockOpaque, Data: raw}
}

func stop(reason sdk.StopReason) string {
	switch reason {
	case sdk.StopReasonEndTurn, sdk.StopReasonStopSequence:
		return protocol.StopEnd
	case sdk.StopReasonToolUse:
		return protocol.StopToolCalls
	case sdk.StopReasonMaxTokens:
		return protocol.StopMaxTokens
	case sdk.StopReasonRefusal:
		return protocol.StopRefusal
	}

	return string(reason)
}
