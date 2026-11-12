// Package protocol defines the two wire contracts of rumbo. Both are JSON-RPC
// 2.0, one JSON object per line.
//
// The Agent Provider Protocol connects the host to a provider process that
// talks to a model. It is specified in docs/protocol.md; this file holds its
// types and serve.go a server for providers written in Go.
//
// The Agent Frontend Protocol connects the host to a front end. It is
// specified in docs/frontend-protocol.md and lives in frontend.go.
package protocol

import (
	"encoding/json"
	"errors"
)

// Version is the protocol version this package implements.
const Version = 1

// Method names.
const (
	MethodInitialize = "initialize"
	MethodComplete   = "complete"
	MethodInvoke     = "invoke"
	MethodCancel     = "cancel"
	MethodDelta      = "delta"
	MethodShutdown   = "shutdown"
)

// Block types.
const (
	BlockText       = "text"
	BlockToolCall   = "tool_call"
	BlockToolResult = "tool_result"
	BlockOpaque     = "opaque"
)

// Stop reasons.
const (
	StopEnd       = "end"
	StopToolCalls = "tool_calls"
	StopMaxTokens = "max_tokens"
	StopRefusal   = "refusal"
)

// Roles.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Block is one piece of message content.
//
//   - text: Text.
//   - tool_call: ID, Name, Args (assistant only).
//   - tool_result: ID of the call it answers, Text, IsError (user only).
//   - opaque: Data the provider needs back verbatim on later requests, such as
//     signed reasoning. The host stores and replays it without reading it.
type Block struct {
	Type    string          `json:"type"`
	Text    string          `json:"text,omitempty"`
	ID      string          `json:"id,omitempty"`
	Name    string          `json:"name,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Message is one turn of the conversation.
type Message struct {
	Role    string  `json:"role"`
	Content []Block `json:"content"`
}

// ToolDef declares a tool the model may call. Schema is a JSON Schema object.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

// Usage reports the tokens of one completion.
type Usage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read,omitempty"`
	CacheWrite int64 `json:"cache_write,omitempty"`
}

// InitializeParams opens the connection. Config is the provider's own
// configuration as registered in the host; the host does not interpret it.
type InitializeParams struct {
	Protocol int             `json:"protocol"`
	Config   json.RawMessage `json:"config,omitempty"`
}

// InitializeResult identifies the provider and carries what it says about
// itself.
type InitializeResult struct {
	Protocol int    `json:"protocol"`
	Name     string `json:"name"`
	Description
}

// Description is how a provider tells the host and the front end what can be
// chosen and asked of it. The host does not interpret any of it: it passes
// the description to the front end, the chosen options to the provider with
// every request, and function calls through. All of it is optional.
type Description struct {
	// Model says how a model is chosen, usually by naming the function that
	// lists them. Its Name is ignored.
	Model *Option `json:"model,omitempty"`
	// Options are what the provider accepts with a request, beyond the model.
	Options []Option `json:"options,omitempty"`
	// Functions are what the provider can be asked through invoke.
	Functions []Function `json:"functions,omitempty"`
}

// Option types.
const (
	OptionString  = "string"
	OptionNumber  = "number"
	OptionBoolean = "boolean"
)

// Option describes one value that can be chosen. It is a deliberately small
// subset of JSON Schema, so a front end can offer it without a validator.
type Option struct {
	Name        string `json:"name,omitempty"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
	// Enum lists the values a string option accepts, when they are fixed.
	Enum []string `json:"enum,omitempty"`
	// Values names the function that returns the accepted values, when they
	// are not fixed. The function answers with a ValuesResult.
	Values string `json:"values,omitempty"`
	// Default is the value in force when none is chosen, for display.
	Default json.RawMessage `json:"default,omitempty"`
}

// Function describes one operation of the provider.
type Function struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Params      []Option `json:"params,omitempty"`
}

// InvokeParams calls a function of the provider.
type InvokeParams struct {
	Name   string          `json:"name"`
	Params json.RawMessage `json:"params,omitempty"`
}

// ValuesResult is the answer of a function that an Option names in Values.
// Any other function answers with whatever JSON it likes.
type ValuesResult struct {
	Values []Value `json:"values"`
}

// Value is one accepted value of an option, with what a person needs to
// recognize it.
type Value struct {
	Value       string `json:"value"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
}

// CompleteParams asks for one assistant message. The host always sends the
// full history, and the history only ever grows by appending, so providers can
// cache the prefix. MaxTokens of zero means the provider's default. Options
// is an object with the values chosen for the options the provider described.
type CompleteParams struct {
	Model     string          `json:"model"`
	System    string          `json:"system"`
	Messages  []Message       `json:"messages"`
	Tools     []ToolDef       `json:"tools,omitempty"`
	MaxTokens int             `json:"max_tokens,omitempty"`
	Options   json.RawMessage `json:"options,omitempty"`
}

// CompleteResult is the finished assistant message.
type CompleteResult struct {
	Message Message `json:"message"`
	Stop    string  `json:"stop"`
	Usage   Usage   `json:"usage"`
}

// DeltaParams streams visible text while a completion is running. Deltas are
// for display only; the CompleteResult is the authoritative message.
type DeltaParams struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
}

// CancelParams asks the provider to abort the completion with the given id.
type CancelParams struct {
	ID int64 `json:"id"`
}

// Frame is a JSON-RPC 2.0 message: a request (ID and Method), a notification
// (Method only), or a response (ID and Result or Error).
type Frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return e.Message
}

// Error codes. CodeTransient is the one a provider chooses: it marks a
// failure worth retrying, such as a rate limit, an overloaded server, or a
// dropped connection. The host waits and sends the request again.
const (
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
	CodeTransient      = 1
)

// Transient wraps err as a failure the host should retry.
func Transient(err error) error {
	return &Error{Code: CodeTransient, Message: err.Error()}
}

// IsTransient reports whether err is a failure the host should retry.
func IsTransient(err error) bool {
	var failure *Error

	return errors.As(err, &failure) && failure.Code == CodeTransient
}
