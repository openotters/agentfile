// Toolinfo pulls a tool image from a registry and prints its metadata (bin path, description, usage).
//
// Usage:
//
//	go run ./examples/tool/toolinfo/ [-plain-http] <registry-ref>
//
// Example:
//
//	go run ./examples/tool/toolinfo/ ghcr.io/openotters/tools/wget:0.1.0
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/openotters/agentfile/pkg/tool"
	"github.com/openotters/agentfile/pkg/utils"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
)

func main() {
	plainHTTP := flag.Bool("plain-http", false, "use plain HTTP instead of HTTPS")
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: toolinfo [-plain-http] <registry-ref>")
		os.Exit(1)
	}

	ref := args[0]

	var opts []utils.RemoteRepositoryOption
	if *plainHTTP {
		opts = append(opts, utils.WithPlainHTTP)
	}

	repo, err := utils.NewRemoteRepository(ref, opts...)
	if err != nil {
		fatal(err)
	}

	tag := repo.Reference.Reference
	if tag == "" {
		tag = "latest"
	}

	store := memory.New()

	_, err = oras.Copy(context.Background(), repo, tag, store, tag, oras.DefaultCopyOptions)
	if err != nil {
		fatal(err)
	}

	manifest, err := utils.ResolveManifest(context.Background(), repo, must(repo.Resolve(context.Background(), tag)))
	if err != nil {
		fatal(err)
	}

	info := tool.Info(*manifest)

	fmt.Printf("bin:         %s\n", info.BinPath)
	fmt.Printf("description: %s\n", info.Description)
	fmt.Printf("usage path:  %s\n", info.UsagePath)
	fmt.Printf("layers:      %d\n", len(info.Layers))

	for _, l := range info.Layers {
		title := l.Title
		if title == "" {
			title = "(untitled)"
		}

		fmt.Printf("  %-20s %s  %d bytes  %s\n", title, l.MediaType, l.Size, l.Digest[:19])
	}

	if info.UsagePath != "" {
		usage, usageErr := tool.FetchUsage(context.Background(), store, *manifest)
		if usageErr != nil {
			fatal(usageErr)
		}

		fmt.Printf("\n--- USAGE.md ---\n%s\n", usage)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		fatal(err)
	}

	return v
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
