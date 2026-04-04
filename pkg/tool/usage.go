package tool

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
)

// FetchUsage fetches the USAGE.md content from fetcher, if the manifest declares one.
// Returns empty string if no usage annotation is present.
func FetchUsage(ctx context.Context, fetcher content.Fetcher, manifest v1.Manifest) (string, error) {
	info := Info(manifest)
	if info.UsagePath == "" {
		return "", nil
	}

	return fetchLayerContent(ctx, fetcher, manifest.Layers, info.UsagePath)
}

func fetchLayerContent(
	ctx context.Context, fetcher content.Fetcher, layers []v1.Descriptor, path string,
) (string, error) {
	for _, layer := range layers {
		title := layer.Annotations[v1.AnnotationTitle]
		if title == path || filepath.Base(title) == filepath.Base(path) {
			rc, err := fetcher.Fetch(ctx, layer)
			if err != nil {
				return "", fmt.Errorf("fetching %s: %w", path, err)
			}
			defer rc.Close()

			data, err := io.ReadAll(rc)
			if err != nil {
				return "", err
			}

			return string(data), nil
		}
	}

	return "", fmt.Errorf("layer %s not found", path)
}
