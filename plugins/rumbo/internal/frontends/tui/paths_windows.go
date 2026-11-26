//go:build windows

package tui

import (
	"net/url"
	"path/filepath"
	"strings"
)

// backslashEscapes says a backslash in a pasted path keeps the character
// after it: on Windows it separates the folders instead.
const backslashEscapes = false

// fileAddressPath is the path a file:// address names: file:///C:/x is
// C:\x.
func fileAddressPath(address *url.URL) string {
	return filepath.FromSlash(strings.TrimPrefix(address.Path, "/"))
}
