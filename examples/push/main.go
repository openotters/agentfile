// Push parses an Agentfile, builds the OCI artifact, and pushes it to a registry using oras.
// For advanced usage (custom auth, retries, middleware), create your own oras.Target
// and use oras.Copy directly.
//
// Usage:
//
//	go run ./examples/agentfile/push/ <path-to-Agentfile> <registry-ref>
//	go run ./examples/agentfile/push/ -plain-http <path-to-Agentfile> <registry-ref>
//
// Example:
//
//	go run ./examples/agentfile/push/ demo/meteo/Agentfile ghcr.io/openotters/agents/meteo:1.0.0
//	go run ./examples/agentfile/push/ -plain-http demo/meteo/Agentfile localhost:5000/agents/meteo:1.0.0
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/openotters/agentfile/build"
	"github.com/openotters/agentfile/oci"
	"oras.land/oras-go/v2"
)

func main() {
	plainHTTP := flag.Bool("plain-http", false, "use plain HTTP instead of HTTPS")
	flag.Parse()

	args := flag.Args()
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: push [-plain-http] <Agentfile> <registry-ref>")
		os.Exit(1)
	}

	path := args[0]
	ref := args[1]

	_, store, digest, err := build.FromFile(context.Background(), path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var opts []oci.RemoteRepositoryOption
	if *plainHTTP {
		opts = append(opts, oci.WithPlainHTTP)
	}

	repo, err := oci.NewRemoteRepository(ref, opts...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	tag := "latest"
	if t := oci.ParseTag(ref); t != "" {
		tag = t
	}

	_, err = oras.Copy(context.Background(), store, "latest", repo, tag, oras.DefaultCopyOptions)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("pushed %s → %s\n", digest, ref)
}
