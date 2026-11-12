// Package netutil has helpers for network configuration.
package netutil

import "strconv"

// ParsePort converts text to a TCP port number.
func ParsePort(text string) int {
	port, err := strconv.Atoi(text)

	if err != nil || port < 1 || port > 65535 {
		panic("invalid port: " + text)
	}

	return port
}
