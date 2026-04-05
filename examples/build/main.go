// Build parses an Agentfile and builds the OCI artifact.
// If a registry reference is provided, the artifact is pushed using Docker credentials.
//
// Usage:
//
//	go run ./examples/build/ <path-to-Agentfile>
//	go run ./examples/build/ <path-to-Agentfile> <registry-ref>
//	go run ./examples/build/ -plain-http <path-to-Agentfile> <registry-ref>
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/openotters/agentfile/build"
	"github.com/openotters/agentfile/oci"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
)

func main() {
	plainHTTP := flag.Bool("plain-http", false, "use plain HTTP instead of HTTPS")
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: build [-plain-http] <Agentfile> [registry-ref]")
		os.Exit(1)
	}

	path := args[0]

	af, store, digest, err := build.FromFile(context.Background(), path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var opts []oci.RemoteRepositoryOption
	if *plainHTTP {
		opts = append(opts, oci.WithPlainHTTP)
	}

	fmt.Printf("digest: %s\n", digest)

	if len(args) >= 2 {
		ref := args[1]

		if pushErr := push(store, ref, opts...); pushErr != nil {
			fmt.Fprintln(os.Stderr, pushErr)
			os.Exit(1)
		}

		fmt.Printf("pushed: %s\n", ref)

		return
	}

	fmt.Println()

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	if err := enc.Encode(af); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func push(store *memory.Store, ref string, opts ...oci.RemoteRepositoryOption) error {
	repo, err := oci.NewRemoteRepository(ref, opts...)
	if err != nil {
		return err
	}

	tag := "latest"
	if t := oci.ParseTag(ref); t != "" {
		tag = t
	}

	_, err = oras.Copy(context.Background(), store, "latest", repo, tag, oras.DefaultCopyOptions)

	return err
}
