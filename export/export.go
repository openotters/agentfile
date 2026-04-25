// Package export serializes an OCI artifact stored in a memory store to a
// portable JSON blob and restores it back into a memory store.
package export

import (
	"bytes"
	"context"
	_ "crypto/sha256" // register sha256 for digest validation
	"encoding/json"
	"fmt"
	"io"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/spec"
)

type exportedArtifact struct {
	Manifest v1.Descriptor  `json:"manifest"`
	Blobs    []exportedBlob `json:"blobs"`
}

type exportedBlob struct {
	Descriptor v1.Descriptor `json:"descriptor"`
	Data       []byte        `json:"data"`
}

// Export serializes the manifest, config, and all layers of ref into a
// portable JSON blob. ctx is honored for each store fetch.
func Export(ctx context.Context, store *memory.Store, ref spec.Reference) ([]byte, error) {
	desc, err := store.Resolve(ctx, ref.String())
	if err != nil {
		return nil, fmt.Errorf("resolving manifest: %w", err)
	}

	artifact := exportedArtifact{Manifest: desc}

	manifestData, err := readBlob(ctx, store, desc)
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}

	artifact.Blobs = append(artifact.Blobs, exportedBlob{Descriptor: desc, Data: manifestData})

	var manifest v1.Manifest
	if err = json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}

	configData, err := readBlob(ctx, store, manifest.Config)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	artifact.Blobs = append(artifact.Blobs, exportedBlob{Descriptor: manifest.Config, Data: configData})

	for _, layer := range manifest.Layers {
		layerData, layerErr := readBlob(ctx, store, layer)
		if layerErr != nil {
			return nil, fmt.Errorf("reading layer: %w", layerErr)
		}

		artifact.Blobs = append(artifact.Blobs, exportedBlob{Descriptor: layer, Data: layerData})
	}

	return json.Marshal(artifact)
}

// Import reconstructs a memory store from a blob produced by Export and
// returns the store plus the root manifest digest.
func Import(ctx context.Context, data []byte) (*memory.Store, string, error) {
	var artifact exportedArtifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return nil, "", fmt.Errorf("parsing artifact: %w", err)
	}

	store := memory.New()

	for _, blob := range artifact.Blobs {
		if err := store.Push(ctx, blob.Descriptor, bytes.NewReader(blob.Data)); err != nil {
			return nil, "", fmt.Errorf("importing blob: %w", err)
		}
	}

	if err := store.Tag(ctx, artifact.Manifest, artifact.Manifest.Digest.String()); err != nil {
		return nil, "", fmt.Errorf("tagging: %w", err)
	}

	return store, artifact.Manifest.Digest.String(), nil
}

func readBlob(ctx context.Context, store *memory.Store, desc v1.Descriptor) ([]byte, error) {
	rc, err := store.Fetch(ctx, desc)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	return io.ReadAll(rc)
}
