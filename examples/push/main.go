// Push parses an Agentfile, builds the OCI artifact, and pushes it to a registry using oras.
//
// Usage:
//
//	go run ./examples/push/ [-plain-http] <Agentfile> <registry-ref>
//
// Example:
//
//	go run ./examples/push/ demo/meteo/Agentfile ghcr.io/openotters/agents/meteo:1.0.0
//	go run ./examples/push/ -plain-http demo/meteo/Agentfile localhost:5000/agents/meteo:1.0.0
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/build"
	"github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
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
	ref := spec.ParseReference(args[1])
	ctx := context.Background()

	store := memory.New()

	imageRef, err := build.FromFile(ctx, path, store)
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

	tag := ref.Tag
	if tag == "" {
		tag = spec.DefaultTag
	}

	_, err = oras.Copy(ctx, store, imageRef.Digest.String(), repo, tag, oras.DefaultCopyOptions)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("pushed %s → %s\n", imageRef.Digest, ref)
}
