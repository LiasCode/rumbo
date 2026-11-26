//go:build unix

package tui

import "net/url"

// backslashEscapes says a backslash in a pasted path keeps the character
// after it, as a shell's does.
const backslashEscapes = true

// fileAddressPath is the path a file:// address names.
func fileAddressPath(address *url.URL) string {
	return address.Path
}
