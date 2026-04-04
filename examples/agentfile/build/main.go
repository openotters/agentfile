// Build parses an Agentfile and builds the OCI artifact, printing the digest and config.
//
// Usage:
//
//	go run ./examples/agentfile/build/ <path-to-Agentfile>
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/openotters/agentfile/pkg/agentfile/build"
	"github.com/openotters/agentfile/pkg/agentfile/parse"
	"oras.land/oras-go/v2/content/memory"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: build <path>")
		os.Exit(1)
	}

	path := os.Args[1]

	af, err := parse.ParseFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	srcDir, _ := filepath.Abs(filepath.Dir(path))

	digest, err := build.Build(context.Background(), af, osfs.New(srcDir), memory.New())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("digest: %s\n\n", digest)

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	if err := enc.Encode(af); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
