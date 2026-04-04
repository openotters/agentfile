// Pull downloads an agent artifact from a registry using oras, then loads and
// dumps the Agentfile as JSON.
// For advanced usage (custom auth, retries, middleware), create your own oras.Target
// and use oras.Copy directly.
//
// Usage:
//
//	go run ./examples/agentfile/pull/ <registry-ref>
//	go run ./examples/agentfile/pull/ -plain-http <registry-ref>
//
// Example:
//
//	go run ./examples/agentfile/pull/ ghcr.io/openotters/agents/meteo:1.0.0
//	go run ./examples/agentfile/pull/ -plain-http localhost:5000/agents/meteo:1.0.0
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/openotters/agentfile/pkg/agentfile"
	"github.com/openotters/agentfile/pkg/utils"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
)

func main() {
	plainHTTP := flag.Bool("plain-http", false, "use plain HTTP instead of HTTPS")
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: pull [-plain-http] <registry-ref>")
		os.Exit(1)
	}

	ref := args[0]

	var opts []utils.RemoteRepositoryOption
	if *plainHTTP {
		opts = append(opts, utils.WithPlainHTTP)
	}

	repo, err := utils.NewRemoteRepository(ref, opts...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	tag := repo.Reference.Reference
	if tag == "" {
		tag = "latest"
	}

	store := memory.New()

	desc, err := oras.Copy(context.Background(), repo, tag, store, tag, oras.DefaultCopyOptions)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if tag != "latest" {
		if err := store.Tag(context.Background(), desc, "latest"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	af, err := agentfile.Load(store, "latest")
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
