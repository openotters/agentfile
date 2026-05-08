package docker

import (
	"context"
	"fmt"

	mobyclient "github.com/moby/moby/client"
)

// resolveImageID maps a human-readable Docker ref ("name:tag") to
// its content-addressed image ID. Required because OCI artifacts
// (manifests with `artifactType` set, or with non-image-shaped
// config / layer mediatypes) are NOT addressable by tag through
// `cli.ImageInspect` / `ImageSave` / `ImageRemove` — those APIs
// 404 with "no such image: <tag>". The ID lookup walks
// `cli.ImageList`, which DOES surface artifacts with their
// RepoTags, so we can find the ID for any tag the daemon knows.
//
// Fast-path: when the ref already looks like an ID (sha256:hex or
// short hex), return it verbatim. Otherwise, list and match.
//
// Returns "" + nil when no image matches — callers should treat
// that as ErrRefNotFound.
func resolveImageID(ctx context.Context, cli Client, ref string) (string, error) {
	if isLikelyImageID(ref) {
		return ref, nil
	}

	res, err := cli.ImageList(ctx, mobyclient.ImageListOptions{})
	if err != nil {
		return "", fmt.Errorf("docker: ImageList: %w", err)
	}

	for _, img := range res.Items {
		for _, tag := range img.RepoTags {
			if tag == ref {
				return img.ID, nil
			}
		}
	}

	return "", nil
}

// isLikelyImageID returns true when ref looks like a digest-shaped
// image ID (sha256:hex). We only fast-path the canonical form;
// short prefixes like `c2f731e1` would also work via ImageInspect
// but we prefer the explicit list lookup to avoid surprising tag
// matches.
func isLikelyImageID(ref string) bool {
	const prefix = "sha256:"
	if len(ref) <= len(prefix) {
		return false
	}
	return ref[:len(prefix)] == prefix
}
