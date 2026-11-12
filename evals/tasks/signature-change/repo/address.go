package netutil

import "fmt"

// Address joins a host and a port given as text.
func Address(host, port string) string {
	return fmt.Sprintf("%s:%d", host, ParsePort(port))
}
