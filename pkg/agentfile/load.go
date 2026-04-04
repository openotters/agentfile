package agentfile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"
)

// Load resolves a manifest by ref and deserializes the Agentfile from the config blob.
func Load(store *memory.Store, ref string) (*Agentfile, error) {
	ctx := context.Background()

	desc, err := store.Resolve(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("resolving manifest: %w", err)
	}

	manifestData, err := fetchBytes(ctx, store, desc)
	if err != nil {
		return nil, fmt.Errorf("fetching manifest: %w", err)
	}

	var manifest v1.Manifest
	if err = json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}

	configData, err := fetchBytes(ctx, store, manifest.Config)
	if err != nil {
		return nil, fmt.Errorf("fetching config: %w", err)
	}

	var af Agentfile
	if err = json.Unmarshal(configData, &af); err != nil {
		return nil, fmt.Errorf("parsing agentfile: %w", err)
	}

	return &af, nil
}

func fetchBytes(ctx context.Context, store *memory.Store, desc v1.Descriptor) ([]byte, error) {
	rc, err := store.Fetch(ctx, desc)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	return io.ReadAll(rc)
}
