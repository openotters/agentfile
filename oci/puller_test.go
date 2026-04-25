//nolint:testpackage // direct internal access
package oci

import (
	"bytes"
	"context"
	"io"
	"testing"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/spec"
)

func TestNoopPuller_WritesShellStub(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := NoopPuller()(context.Background(), spec.Reference{Name: "x"}, &buf); err != nil {
		t.Fatalf("NoopPuller: %v", err)
	}

	if got := buf.String(); got != "#!/bin/sh\n" {
		t.Errorf("buffer = %q", got)
	}
}

func TestExtractBin_DirectLayerByTitle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := memory.New()

	bin := []byte("\x7fELFbinary")

	layer, err := pushBlob(ctx, store, "application/vnd.openotters.bin", "hello", bin)
	if err != nil {
		t.Fatal(err)
	}

	manifest := v1.Manifest{
		Annotations: map[string]string{spec.AnnotationBinName: "hello"},
		Layers:      []v1.Descriptor{layer},
	}

	var out bytes.Buffer
	if e := extractBin(ctx, store, manifest, &out); e != nil {
		t.Fatalf("extractBin: %v", e)
	}

	if !bytes.Equal(out.Bytes(), bin) {
		t.Errorf("extracted bytes mismatch")
	}
}

func TestExtractBin_FromTarGz(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := memory.New()

	bin := []byte("binary-contents")

	tarData, err := buildTarGz(map[string][]byte{
		"usr/local/bin/hello": bin,
		"usr/local/bin/other": []byte("other-binary"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Layer doesn't have a matching title -- forces tar traversal.
	layer, err := pushBlob(ctx, store, "application/vnd.oci.image.layer.v1.tar+gzip", "payload.tar.gz", tarData)
	if err != nil {
		t.Fatal(err)
	}

	manifest := v1.Manifest{
		Annotations: map[string]string{
			spec.AnnotationBinName: "hello",
			spec.AnnotationBinPath: "usr/local/bin",
		},
		Layers: []v1.Descriptor{layer},
	}

	var out bytes.Buffer
	if e := extractBin(ctx, store, manifest, &out); e != nil {
		t.Fatalf("extractBin: %v", e)
	}

	if !bytes.Equal(out.Bytes(), bin) {
		t.Errorf("extracted %q, want %q", out.String(), bin)
	}
}

func TestExtractBin_NotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := memory.New()

	tarData, err := buildTarGz(map[string][]byte{
		"usr/local/bin/other": []byte("nope"),
	})
	if err != nil {
		t.Fatal(err)
	}

	layer, err := pushBlob(ctx, store, "application/vnd.oci.image.layer.v1.tar+gzip", "payload.tar.gz", tarData)
	if err != nil {
		t.Fatal(err)
	}

	manifest := v1.Manifest{
		Annotations: map[string]string{
			spec.AnnotationBinName: "hello",
			spec.AnnotationBinPath: "usr/local/bin",
		},
		Layers: []v1.Descriptor{layer},
	}

	if e := extractBin(ctx, store, manifest, io.Discard); e == nil {
		t.Fatal("expected error when bin is missing")
	}
}
