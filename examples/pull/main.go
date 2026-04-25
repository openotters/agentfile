// Pull downloads an agent artifact from a registry using oras, then loads and
// dumps the Agentfile as JSON.
// For advanced usage (custom auth, retries, middleware), create your own oras.Target
// and use oras.Copy directly.
//
// Usage:
//
//	go run ./examples/pull/ <registry-ref>
//	go run ./examples/pull/ -plain-http <registry-ref>
//
// Example:
//
//	go run ./examples/pull/ ghcr.io/openotters/agents/meteo:1.0.0
//	go run ./examples/pull/ -plain-http localhost:5000/agents/meteo:1.0.0
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
	afstore "github.com/openotters/agentfile/store"
)

func main() {
	plainHTTP := flag.Bool("plain-http", false, "use plain HTTP instead of HTTPS")
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: pull [-plain-http] <registry-ref>")
		os.Exit(1)
	}

	ref := spec.ParseReference(args[0])
	ctx := context.Background()

	var opts []oci.RemoteRepositoryOption
	if *plainHTTP {
		opts = append(opts, oci.WithPlainHTTP)
	}

	repo, err := oci.NewRemoteRepository(ref, opts...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	tag := repo.Reference.Reference
	if tag == "" {
		tag = "latest"
	}

	store := memory.New()

	desc, err := oras.Copy(ctx, repo, tag, store, tag, oras.DefaultCopyOptions)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if tag != "latest" {
		if err := store.Tag(ctx, desc, "latest"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	_, af, err := afstore.Load(ctx, store, spec.ParseReference("latest"))
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
