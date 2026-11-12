set -e
cat > port.go <<'GO'
// Package netutil has helpers for network configuration.
package netutil

import (
	"fmt"
	"strconv"
)

// ParsePort converts text to a TCP port number.
func ParsePort(text string) (int, error) {
	port, err := strconv.Atoi(text)

	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid port: %q", text)
	}

	return port, nil
}
GO
cat > address.go <<'GO'
package netutil

import "fmt"

// Address joins a host and a port given as text.
func Address(host, port string) (string, error) {
	number, err := ParsePort(port)

	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%s:%d", host, number), nil
}
GO
cat > cmd/listen/main.go <<'GO'
// Command listen prints the address it would listen on.
package main

import (
	"fmt"
	"os"

	"netutil"
)

func main() {
	text := "8080"

	if len(os.Args) > 1 {
		text = os.Args[1]
	}

	port, err := netutil.ParsePort(text)

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Println("listening on port", port)
}
GO
