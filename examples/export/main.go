// Export parses an Agentfile, builds the OCI artifact, and exports it to a JSON file.
//
// Usage:
//
//	go run ./examples/export/ <path-to-Agentfile> <output.json>
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-git/go-billy/v6/osfs"
	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/build"
	"github.com/openotters/agentfile/export"
	"github.com/openotters/agentfile/spec"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: export <Agentfile> <output.json>")
		os.Exit(1)
	}

	path := os.Args[1]
	output := os.Args[2]

	af, err := spec.ParseFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	srcDir, _ := filepath.Abs(filepath.Dir(path))

	store := memory.New()

	ref, err := build.Build(context.Background(), af, osfs.New(srcDir), store)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	data, err := export.Export(context.Background(), store, ref.Reference)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := os.WriteFile(output, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("exported %s → %s (%d bytes)\n", ref, output, len(data))
}
