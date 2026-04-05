// Import loads an exported agent artifact JSON file.
// Without a registry reference, loads into memory and prints the digest.
// With a registry reference, pushes the artifact to the target repository.
//
// Usage:
//
//	go run ./examples/import/ <input.json>
//	go run ./examples/import/ <input.json> <registry-ref>
//	go run ./examples/import/ -plain-http <input.json> <registry-ref>
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/openotters/agentfile/export"
	"github.com/openotters/agentfile/oci"
	"oras.land/oras-go/v2"
)

func main() {
	plainHTTP := flag.Bool("plain-http", false, "use plain HTTP instead of HTTPS")
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: import [-plain-http] <input.json> [registry-ref]")
		os.Exit(1)
	}

	data, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	store, digest, err := export.Import(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("imported %s\n", digest)

	if len(args) >= 2 {
		ref := args[1]

		var opts []oci.RemoteRepositoryOption
		if *plainHTTP {
			opts = append(opts, oci.WithPlainHTTP)
		}

		repo, repoErr := oci.NewRemoteRepository(ref, opts...)
		if repoErr != nil {
			fmt.Fprintln(os.Stderr, repoErr)
			os.Exit(1)
		}

		tag := "latest"
		if t := oci.ParseTag(ref); t != "" {
			tag = t
		}

		if _, copyErr := oras.Copy(context.Background(), store, "latest", repo, tag, oras.DefaultCopyOptions); copyErr != nil {
			fmt.Fprintln(os.Stderr, copyErr)
			os.Exit(1)
		}

		fmt.Printf("pushed: %s\n", ref)
	}
}
