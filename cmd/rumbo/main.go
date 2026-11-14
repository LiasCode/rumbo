// Command rumbo is a minimal agent harness.
package main

import (
	"os"

	"rumbo/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
