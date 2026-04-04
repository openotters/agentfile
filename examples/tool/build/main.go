// Buildtool-multiarch builds per-platform OCI tool images and assembles an image index.
// Authentication uses Docker credential helpers (~/.docker/config.json).
//
// Usage:
//
//	go run ./examples/tool/buildtool-multiarch/ [-plain-http] -name <name> [-desc <description>] [-usage <text>] <registry-ref> <os/arch:path> [...]
//
// Example:
//
//	go run ./examples/tool/buildtool-multiarch/ -name jq -desc "JSON processor" \
//	  ghcr.io/openotters/tools/jq:0.1.0 \
//	  linux/amd64:bin/jq-linux-amd64 \
//	  linux/arm64:bin/jq-linux-arm64 \
//	  darwin/arm64:bin/jq-darwin-arm64
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/openotters/agentfile/pkg/tool"
	"github.com/openotters/agentfile/pkg/utils"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
)

func main() {
	plainHTTP := flag.Bool("plain-http", false, "use plain HTTP instead of HTTPS")
	name := flag.String("name", "", "tool name (required)")
	desc := flag.String("desc", "", "one-line description")
	usageText := flag.String("usage", "", "usage guidelines text")
	flag.Parse()

	args := flag.Args()
	if *name == "" || len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: buildtool-multiarch [-plain-http] -name <name> [-desc <desc>] [-usage <text>] <ref> <os/arch:path> [...]")
		os.Exit(1)
	}

	ref := args[0]

	var platforms []tool.PlatformBuild

	for _, p := range args[1:] {
		osArch, binPath, ok := strings.Cut(p, ":")
		if !ok {
			fatal(fmt.Errorf("invalid platform spec %q, expected os/arch:path", p))
		}

		goos, goarch, ok := strings.Cut(osArch, "/")
		if !ok {
			fatal(fmt.Errorf("invalid platform %q, expected os/arch", osArch))
		}

		srcDir, _ := filepath.Abs(filepath.Dir(binPath))

		platforms = append(platforms, tool.PlatformBuild{
			OS:   goos,
			Arch: goarch,
			Src:  osfs.New(srcDir),
		})
	}

	store := memory.New()

	digest, err := tool.BuildIndex(context.Background(), tool.BuildOptions{
		Name:        *name,
		BinPath:     filepath.Base(args[1][strings.Index(args[1], ":")+1:]),
		Description: *desc,
		Usage:       *usageText,
	}, platforms, store)
	if err != nil {
		fatal(err)
	}

	var opts []utils.RemoteRepositoryOption
	if *plainHTTP {
		opts = append(opts, utils.WithPlainHTTP)
	}

	repo, err := utils.NewRemoteRepository(ref, opts...)
	if err != nil {
		fatal(err)
	}

	tag := "latest"
	if t := utils.ParseTag(ref); t != "" {
		tag = t
	}

	_, err = oras.Copy(context.Background(), store, "latest", repo, tag, oras.DefaultCopyOptions)
	if err != nil {
		fatal(fmt.Errorf("pushing: %w", err))
	}

	fmt.Printf("built %s (%d platforms) → %s (%s)\n", *name, len(platforms), ref, digest)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
