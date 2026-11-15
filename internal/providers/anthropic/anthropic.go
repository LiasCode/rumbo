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
	"os"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/LiasCode/rumbo/internal/providers/credentials"
	"github.com/LiasCode/rumbo/protocol"
)

// Streaming removes the timeout concern, so the default leaves room to think
// and to write whole files.
const defaultMaxTokens = 64_000

// Config is what the provider accepts on initialize. Credentials are not
// part of it: they come from the environment (ANTHROPIC_API_KEY,
// ANTHROPIC_AUTH_TOKEN), then from the key login stored, and last from the
// profile created by "ant auth login".
type Config struct {
	BaseURL string `json:"base_url"`
	// Effort is one of low, medium, high, xhigh, max, sent when a request
	// chooses none as an option. Empty uses the model's default.
	Effort string `json:"effort"`
}

// Handler implements protocol.Handler on the Messages API.
type Handler struct {
	client sdk.Client
	config Config
	// windows holds the context size of each model asked about.
	windows map[string]int64
	// keys holds the API keys login stored, by server.
	keys credentials.Store
	// environment names the variable the credential comes from, read when
	// the client is made, as the SDK reads it; empty when none gives one.
	environment string
}

// New returns a handler that is ready after Initialize.
func New() *Handler {
	return &Handler{windows: map[string]int64{}, keys: credentials.Default("anthropic")}
}

// defaultServer is where the API is when nothing says otherwise.
const defaultServer = "https://api.anthropic.com"

// server is the address of the API this provider reaches, which a stored key
// belongs to.
func (h *Handler) server() string {
	for _, address := range []string{h.config.BaseURL, os.Getenv("ANTHROPIC_BASE_URL")} {
		if address != "" {
			return strings.TrimRight(address, "/")
		}
	}

	return defaultServer
}

// fromEnvironment names the variable that gives the credential, which comes
// before a stored key, or is empty when none does.
func fromEnvironment() string {
	for _, name := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		if os.Getenv(name) != "" {
			return name
		}
	}

	return ""
}

// credential gives a request the key login stored, when the environment
// gives none. Without either, the SDK's own chain applies, such as the
// profile from "ant auth login". It is read for every request, so a login
// made in another process of this provider applies at once.
func (h *Handler) credential() ([]option.RequestOption, error) {
	if h.environment != "" {
		return nil, nil
	}

	key, err := h.keys.APIKey(h.server())

	if err != nil || key == "" {
		return nil, err
	}

	return []option.RequestOption{option.WithAPIKey(key)}, nil
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
	h.environment = fromEnvironment()

	// Every field is said, given or not, so the host can keep each with
	// its value. An empty one is left to the SDK and the model.
	config, _ := json.Marshal(h.config)

	return protocol.InitializeResult{Protocol: protocol.Version, Name: "anthropic", Config: config, Description: description}, nil
}

// description is what this provider lets the user choose and ask.
var description = protocol.Description{
	Model: &protocol.Option{Type: protocol.OptionString, Values: "models", Description: "Claude model to use"},
	Options: []protocol.Option{{
		Name:        "effort",
		Type:        protocol.OptionString,
		Enum:        []string{"low", "medium", "high", "xhigh", "max"},
		Description: "How much the model thinks and how thorough it is. Empty, the effort in the provider's configuration applies, or else the model's own default.",
		Default:     json.RawMessage(`""`),
	}},
	Functions: []protocol.Function{
		{Name: "models", Description: "List the models this account can use"},
		{Name: "login", Description: "Store an API key for this provider, once it is checked to work"},
		{
			Name:        "logout",
			Description: "Remove the API key login stored",
			Confirm:     "This removes the API key that login stored for this provider. A key in the environment, and the profile from ant auth login, are not touched.",
		},
		{Name: "account", Description: "Say which credential this provider uses, and where it comes from"},
	},
}

// options are the values a request may carry, as described above.
type options struct {
	Effort string `json:"effort"`
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

func (h *Handler) models(ctx context.Context) (protocol.ValuesResult, error) {
	var result protocol.ValuesResult

	using, err := h.credential()

	if err != nil {
		return result, err
	}

	pages := h.client.Models.ListAutoPaging(ctx, sdk.ModelListParams{}, using...)

	for pages.Next() {
		model := pages.Current()
		result.Values = append(result.Values, protocol.Value{Value: model.ID, Label: model.DisplayName})
	}

	return result, pages.Err()
}

// login asks for an API key, checks it with a request that costs nothing,
// and stores it for this provider's server.
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

	if _, err := h.client.Models.List(ctx, sdk.ModelListParams{Limit: sdk.Int(1)}, option.WithAPIKey(key)); err != nil {
		return "", fmt.Errorf("the key was not stored: %w", err)
	}

	if err := h.keys.SetAPIKey(h.server(), key); err != nil {
		return "", err
	}

	text := fmt.Sprintf("logged in to %s; the key is kept in %s, which only you can read", h.server(), h.keys.Path)

	if h.environment != "" {
		text += fmt.Sprintf(". %s is set in the environment and still comes first", h.environment)
	}

	return text, nil
}

// logout forgets the key stored for this provider's server.
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

// account says where the credential of the next request comes from,
// without showing it.
func (h *Handler) account() (string, error) {
	stored, err := h.keys.APIKey(h.server())

	if err != nil {
		return "", err
	}

	switch name := h.environment; {
	case name != "" && stored != "":
		return fmt.Sprintf("%s: using %s from the environment, which comes before the key login stored", h.server(), name), nil
	case name != "":
		return fmt.Sprintf("%s: using %s from the environment", h.server(), name), nil
	case stored != "":
		return fmt.Sprintf("%s: using the key login stored, in %s", h.server(), h.keys.Path), nil
	}

	return fmt.Sprintf(`%s: no key in the environment or stored by login; the profile from "ant auth login" is used, if there is one`, h.server()), nil
}

// Complete implements protocol.Handler.
func (h *Handler) Complete(ctx context.Context, params protocol.CompleteParams, emit func(text string)) (protocol.CompleteResult, error) {
	request, err := h.request(params)

	if err != nil {
		return protocol.CompleteResult{}, err
	}

	using, err := h.credential()

	if err != nil {
		return protocol.CompleteResult{}, err
	}

	stream := h.client.Messages.NewStreaming(ctx, request, using...)
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

	completed := result(message)
	completed.ContextWindow = h.window(ctx, params.Model, using)

	return completed, nil
}

// window returns how many tokens the model's context holds, as the API says.
// It is asked once per model. Zero means it could not be found out, which
// only makes the host fall back to a fixed limit.
func (h *Handler) window(ctx context.Context, model string, using []option.RequestOption) int64 {
	if size, known := h.windows[model]; known {
		return size
	}

	var size int64

	if info, err := h.client.Models.Get(ctx, model, sdk.ModelGetParams{}, using...); err == nil {
		size = info.MaxInputTokens
	}

	h.windows[model] = size

	return size
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

		// The API has no code of its own for a prompt that does not fit, so
		// its message is what tells it apart from other bad requests.
		case status == 413, status == 400 && strings.Contains(strings.ToLower(failure.Error()), "prompt is too long"):
			return protocol.Overflow(err)
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
	var chosen options

	if len(params.Options) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(params.Options))
		decoder.DisallowUnknownFields()

		if err := decoder.Decode(&chosen); err != nil {
			return request, fmt.Errorf("invalid options: %w", err)
		}
	}

	// An empty effort is none chosen: the host sends the option's default,
	// "", with every request, which would otherwise hide the configured one.
	if chosen.Effort == "" {
		chosen.Effort = h.config.Effort
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
