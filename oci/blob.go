package oci

import (
	"context"
	"fmt"
	"io"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry/remote"
)

func ParseTag(ref string) string {
	for i := len(ref) - 1; i >= 0; i-- {
		if ref[i] == ':' {
			return ref[i+1:]
		}

		if ref[i] == '/' {
			return ""
		}
	}

	return ""
}

func FetchBlobBytes(ctx context.Context, repo *remote.Repository, desc v1.Descriptor) ([]byte, error) {
	rc, err := repo.Fetch(ctx, desc)
	if err != nil {
		return nil, fmt.Errorf("fetching blob: %w", err)
	}
	defer rc.Close()

	return io.ReadAll(rc)
}
