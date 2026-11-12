// Package provider is the host side of the Agent Provider Protocol. The agent
// only knows the Provider interface; Stdio is the implementation that runs a
// provider process.
package provider

import (
	"context"

	"rumbo/protocol"
)

// Provider produces assistant messages.
type Provider interface {
	Complete(ctx context.Context, params protocol.CompleteParams, onDelta func(text string)) (protocol.CompleteResult, error)
	Close() error
}
