// Import loads an exported agent artifact JSON file and prints its manifest digest.
//
// Usage:
//
//	go run ./examples/agentfile/import/ <input.json>
package main

import (
	"fmt"
	"os"

	"github.com/openotters/agentfile/pkg/agentfile/export"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: import <input.json>")
		os.Exit(1)
	}

	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	_, digest, err := export.Import(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("imported %s\n", digest)
}
