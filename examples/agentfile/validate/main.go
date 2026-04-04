// Validate checks whether an Agentfile is syntactically and semantically valid.
//
// Usage:
//
//	go run ./examples/agentfile/validate/ <path-to-Agentfile>
package main

import (
	"fmt"
	"os"

	"github.com/openotters/agentfile/pkg/agentfile/validate"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: validate <path>")
		os.Exit(1)
	}

	if validate.ValidateFile(os.Args[1]) {
		fmt.Println("valid")
	} else {
		fmt.Println("invalid")
		os.Exit(1)
	}
}
