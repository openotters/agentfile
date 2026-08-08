package oci

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"

	"github.com/openotters/agentfile/spec"
)

// UsageFetcher fetches the USAGE.md body declared by a BIN image at
// ref. Returns the empty string with a nil error when the image
// carries no `io.openotters.bin.usage` annotation (or the annotation
// references a layer that isn't present). Errors are reserved for
// network / authentication / parse failures, so callers can treat
// an absent doc as a no-op without distinguishing it from a real
// failure path.
//
// Parallel in shape to Puller: a function value the consumer wires
// into the executor pipeline — `RemoteUsageFetcher` for production, or
// nil to skip usage fetching entirely (tests / offline mode; the
// materialiser treats a nil fetcher as "no usage docs").
type UsageFetcher func(ctx context.Context, ref spec.Reference) (string, error)

// RemoteUsageFetcher mirrors RemotePuller for the documentation
// layer: resolves ref against a real registry, parses the manifest,
// and returns the body of the layer pointed at by the
// `io.openotters.bin.usage` annotation. The annotation defaults to
// `/USAGE.md` (see spec.DefaultUsagePath) but producers may pick a
// different path; this fetcher honours whatever the producer wrote.
func RemoteUsageFetcher(platform v1.Platform, opts ...RemoteRepositoryOption) UsageFetcher {
	return func(ctx context.Context, ref spec.Reference) (string, error) {
		repo, err := NewRemoteRepository(ref, opts...)
		if err != nil {
			return "", err
		}

		tag := repo.Reference.Reference
		if tag == "" {
			tag = "latest"
		}

		desc, err := repo.Resolve(ctx, tag)
		if err != nil {
			return "", fmt.Errorf("resolving %s: %w", ref, err)
		}

		manifest, err := ResolveManifest(ctx, repo, desc, platform)
		if err != nil {
			return "", err
		}

		return extractUsage(ctx, repo, *manifest)
	}
}

// extractUsage walks the manifest looking for the layer whose
// AnnotationTitle matches the manifest's `io.openotters.bin.usage`
// annotation. Both an exact title match and a basename match are
// accepted because producers historically wrote either form.
func extractUsage(
	ctx context.Context, fetcher content.Fetcher, manifest v1.Manifest,
) (string, error) {
	path, ok := manifest.Annotations[spec.AnnotationBinUsage]
	if !ok || path == "" {
		return "", nil
	}

	for _, layer := range manifest.Layers {
		title := layer.Annotations[v1.AnnotationTitle]
		if title != path && filepath.Base(title) != filepath.Base(path) {
			continue
		}

		rc, err := fetcher.Fetch(ctx, layer)
		if err != nil {
			return "", fmt.Errorf("fetching usage layer %s: %w", path, err)
		}
		defer rc.Close()

		data, err := io.ReadAll(rc)
		if err != nil {
			return "", fmt.Errorf("reading usage layer %s: %w", path, err)
		}

		return string(data), nil
	}

	// Annotation present but no matching layer — older images that
	// declared usage in the manifest without ever pushing the blob.
	// Treat as absent rather than erroring; callers see "no doc".
	return "", nil
}
