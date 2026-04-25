//nolint:testpackage // direct internal access
package oci

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"
)

// pushJSON marshals v, pushes it into store with mediaType, and returns a
// descriptor pointing at it.
func pushJSON(ctx context.Context, store *memory.Store, mediaType string, v any) (v1.Descriptor, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return v1.Descriptor{}, err
	}

	desc := v1.Descriptor{
		MediaType: mediaType,
		Digest:    digest.FromBytes(data),
		Size:      int64(len(data)),
	}

	if e := store.Push(ctx, desc, bytes.NewReader(data)); e != nil {
		return v1.Descriptor{}, e
	}

	return desc, nil
}

// pushBlob pushes raw bytes as a blob with mediaType and an AnnotationTitle.
func pushBlob(ctx context.Context, store *memory.Store, mediaType, title string, data []byte) (v1.Descriptor, error) {
	desc := v1.Descriptor{
		MediaType:   mediaType,
		Digest:      digest.FromBytes(data),
		Size:        int64(len(data)),
		Annotations: map[string]string{v1.AnnotationTitle: title},
	}

	if e := store.Push(ctx, desc, bytes.NewReader(data)); e != nil {
		return v1.Descriptor{}, e
	}

	return desc, nil
}

// buildTarGz builds an in-memory gzipped tar archive containing the given
// files (name → content).
func buildTarGz(files map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o755,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			return nil, err
		}

		if _, err := tw.Write(content); err != nil {
			return nil, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}

	if err := gz.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
