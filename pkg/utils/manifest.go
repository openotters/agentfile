package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry/remote"
)

func ResolveManifest(ctx context.Context, repo *remote.Repository, desc v1.Descriptor) (*v1.Manifest, error) {
	data, err := FetchBlobBytes(ctx, repo, desc)
	if err != nil {
		return nil, fmt.Errorf("fetching manifest: %w", err)
	}

	if platformDesc, ok := resolveIndex(data); ok {
		return ResolveManifest(ctx, repo, platformDesc)
	}

	var manifest v1.Manifest
	if err = json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}

	return &manifest, nil
}

func resolveIndex(data []byte) (v1.Descriptor, bool) {
	var index v1.Index
	if err := json.Unmarshal(data, &index); err != nil || len(index.Manifests) == 0 {
		return v1.Descriptor{}, false
	}

	for _, m := range index.Manifests {
		if m.Platform != nil &&
			m.Platform.Architecture == runtime.GOARCH &&
			m.Platform.OS == runtime.GOOS {
			return m, true
		}
	}

	for _, m := range index.Manifests {
		if m.Platform != nil && m.Platform.Architecture == runtime.GOARCH {
			return m, true
		}
	}

	return index.Manifests[0], true
}
