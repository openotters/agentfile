package store

import (
	"context"
	"encoding/json"
	"fmt"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"
)

// Manifest resolves and returns the OCI manifest from a store by ref (e.g. "latest", "v1.0").
func Manifest(store *memory.Store, ref string) (*v1.Manifest, error) {
	ctx := context.Background()

	desc, err := store.Resolve(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("resolving manifest: %w", err)
	}

	data, err := fetchBytes(ctx, store, desc)
	if err != nil {
		return nil, fmt.Errorf("fetching manifest: %w", err)
	}

	var manifest v1.Manifest
	if err = json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}

	return &manifest, nil
}

// Layers returns all layers matching the given media type.
func Layers(manifest *v1.Manifest, mediaType string) []v1.Descriptor {
	var result []v1.Descriptor

	for _, l := range manifest.Layers {
		if l.MediaType == mediaType {
			result = append(result, l)
		}
	}

	return result
}

// FetchLayer fetches a layer's content by descriptor from a store.
func FetchLayer(store *memory.Store, desc v1.Descriptor) ([]byte, error) {
	return fetchBytes(context.Background(), store, desc)
}
