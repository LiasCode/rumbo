// Package provider is the host side of the Agent Provider Protocol. The agent
// only knows the Provider interface. Stdio runs one provider process; Pool
// keeps as many as the requests running at once need.
package provider

import (
	"context"
	"encoding/json"

	"github.com/LiasCode/rumbo/protocol"
)

// Provider produces assistant messages. It also says what can be chosen and
// asked of it, which the host passes on without interpreting.
type Provider interface {
	Complete(ctx context.Context, params protocol.CompleteParams, onDelta func(text string)) (protocol.CompleteResult, error)
	Description() protocol.Description
	// Invoke calls a function of the provider, which talks with the person
	// through dialog while it works. A nil dialog lets it say and ask
	// nothing.
	Invoke(ctx context.Context, name string, params json.RawMessage, dialog protocol.Dialog) (json.RawMessage, error)
	Close() error
}
