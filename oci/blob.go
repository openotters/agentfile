package oci

import (
	"context"
	"fmt"
	"io"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"

	"github.com/openotters/agentfile/spec"
)

// ParseTag returns the tag component of a reference.
func ParseTag(ref spec.Reference) string {
	return ref.Tag
}

// FetchBlobBytes reads the blob identified by desc from fetcher into memory.
// Accepts any content.Fetcher so it can be exercised against in-memory
// stores in tests as well as remote repositories.
func FetchBlobBytes(ctx context.Context, fetcher content.Fetcher, desc v1.Descriptor) ([]byte, error) {
	rc, err := fetcher.Fetch(ctx, desc)
	if err != nil {
		return nil, fmt.Errorf("fetching blob: %w", err)
	}
	defer rc.Close()

	return io.ReadAll(rc)
}
