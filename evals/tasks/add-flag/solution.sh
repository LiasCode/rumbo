set -e
cat > main.go <<'GO'
// Command wordcount prints the number of words read from standard input.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	asJSON := flag.Bool("json", false, "print a JSON object")
	flag.Parse()

	words, lines := 0, 0
	scanner := bufio.NewScanner(os.Stdin)

	for scanner.Scan() {
		lines++
		words += len(strings.Fields(scanner.Text()))
	}

	if *asJSON {
		json.NewEncoder(os.Stdout).Encode(map[string]int{"words": words, "lines": lines})
		return
	}

	fmt.Println(words)
}
GO
