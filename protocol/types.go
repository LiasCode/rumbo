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

import "encoding/json"

// Version is the protocol version this package implements.
const Version = 1

// Method names.
const (
	MethodInitialize = "initialize"
	MethodComplete   = "complete"
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

// InitializeResult identifies the provider.
type InitializeResult struct {
	Protocol int    `json:"protocol"`
	Name     string `json:"name"`
}

// CompleteParams asks for one assistant message. The host always sends the
// full history, and the history only ever grows by appending, so providers can
// cache the prefix. MaxTokens of zero means the provider's default.
type CompleteParams struct {
	Model     string    `json:"model"`
	System    string    `json:"system"`
	Messages  []Message `json:"messages"`
	Tools     []ToolDef `json:"tools,omitempty"`
	MaxTokens int       `json:"max_tokens,omitempty"`
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

// Error codes.
const (
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
)
