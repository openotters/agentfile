// Export parses an Agentfile, builds the OCI artifact, and exports it to a JSON file.
//
// Usage:
//
//	go run ./examples/agentfile/export/ <path-to-Agentfile> <output.json>
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/openotters/agentfile/pkg/agentfile/build"
	"github.com/openotters/agentfile/pkg/agentfile/export"
	"github.com/openotters/agentfile/pkg/agentfile/parse"
	"oras.land/oras-go/v2/content/memory"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: export <Agentfile> <output.json>")
		os.Exit(1)
	}

	path := os.Args[1]
	output := os.Args[2]

	af, err := parse.ParseFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	srcDir, _ := filepath.Abs(filepath.Dir(path))

	store := memory.New()

	digest, err := build.Build(context.Background(), af, osfs.New(srcDir), store)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	data, err := export.Export(store)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := os.WriteFile(output, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("exported %s → %s (%d bytes)\n", digest, output, len(data))
}
