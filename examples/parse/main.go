// Parse reads an Agentfile and dumps the parsed structure as JSON.
//
// Usage:
//
//	go run ./examples/agentfile/parse/ <path-to-Agentfile>
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/openotters/agentfile/spec"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: parse <path>")
		os.Exit(1)
	}

	af, err := spec.ParseFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	if err := enc.Encode(af); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
