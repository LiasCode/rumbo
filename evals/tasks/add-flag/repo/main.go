// Command wordcount prints the number of words read from standard input.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func main() {
	words := 0
	scanner := bufio.NewScanner(os.Stdin)

	for scanner.Scan() {
		words += len(strings.Fields(scanner.Text()))
	}

	fmt.Println(words)
}
