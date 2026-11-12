// Command listen prints the address it would listen on.
package main

import (
	"fmt"
	"os"

	"netutil"
)

func main() {
	port := "8080"

	if len(os.Args) > 1 {
		port = os.Args[1]
	}

	fmt.Println("listening on port", netutil.ParsePort(port))
}
