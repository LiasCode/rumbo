// Package provider is the host side of the Agent Provider Protocol. The agent
// only knows the Provider interface. Stdio runs one provider process; Pool
// keeps as many as the requests running at once need.
package provider

import (
	"context"
	"encoding/json"

	"rumbo/protocol"
)

// Provider produces assistant messages. It also says what can be chosen and
// asked of it, which the host passes on without interpreting.
type Provider interface {
	Complete(ctx context.Context, params protocol.CompleteParams, onDelta func(text string)) (protocol.CompleteResult, error)
	Description() protocol.Description
	Invoke(ctx context.Context, name string, params json.RawMessage) (json.RawMessage, error)
	Close() error
}
