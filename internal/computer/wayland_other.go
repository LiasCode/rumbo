//go:build !linux

package computer

import (
	"context"
	"errors"
)

// openWayland is Linux's alone: its client speaks to wlroots compositors.
func openWayland(context.Context) (Screen, error) {
	return nil, errors.New("the computer tool drives Wayland only on Linux")
}
