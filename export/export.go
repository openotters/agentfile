package export

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	_ "crypto/sha256" // register sha256 for digest validation

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"
)

type exportedArtifact struct {
	Manifest v1.Descriptor  `json:"manifest"`
	Blobs    []exportedBlob `json:"blobs"`
}

type exportedBlob struct {
	Descriptor v1.Descriptor `json:"descriptor"`
	Data       []byte        `json:"data"`
}

func Export(store *memory.Store) ([]byte, error) {
	ctx := context.Background()

	desc, err := store.Resolve(ctx, "latest")
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

func Import(data []byte) (*memory.Store, string, error) {
	var artifact exportedArtifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return nil, "", fmt.Errorf("parsing artifact: %w", err)
	}

	store := memory.New()
	ctx := context.Background()

	for _, blob := range artifact.Blobs {
		if err := store.Push(ctx, blob.Descriptor, bytes.NewReader(blob.Data)); err != nil {
			return nil, "", fmt.Errorf("importing blob: %w", err)
		}
	}

	if err := store.Tag(ctx, artifact.Manifest, "latest"); err != nil {
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
