// Validate checks whether an Agentfile is syntactically and semantically valid.
//
// Usage:
//
//	go run ./examples/validate/ <path-to-Agentfile>
package main

import (
	"fmt"
	"os"

	"github.com/openotters/agentfile/validate"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: validate <path>")
		os.Exit(1)
	}

	if err := validate.ValidateFile(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Println("valid")
}
