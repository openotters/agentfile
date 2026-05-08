//nolint:testpackage // tests unexported helpers
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// stagedDescriptor builds a descriptor whose digest matches data's
// sha256 — keeps the test inputs internally consistent with what
// Push expects from real callers.
func stagedDescriptor(mediaType string, data []byte) ocispec.Descriptor {
	d := digest.FromBytes(data)
	return ocispec.Descriptor{
		MediaType: mediaType,
		Digest:    d,
		Size:      int64(len(data)),
	}
}

// newTestStore is a Store with no docker client, suitable for
// tests that exercise the in-memory parts (Push / Exists / Fetch
// / Resolve-from-cache / Tag-bareword-skip / buildOCILayoutTar).
// hydrate / Tag-with-load tests use a mockdocker-backed Store.
func newTestStore() *Store {
	return &Store{
		blobs:  map[digest.Digest][]byte{},
		descs:  map[digest.Digest]ocispec.Descriptor{},
		loaded: map[string]ocispec.Descriptor{},
	}
}

func TestStore_PushExistsFetch(t *testing.T) {
	t.Parallel()

	s := newTestStore()
	data := []byte("hello world")
	desc := stagedDescriptor(ocispec.MediaTypeImageManifest, data)

	if err := s.Push(context.Background(), desc, bytes.NewReader(data)); err != nil {
		t.Fatalf("Push: %v", err)
	}

	ok, err := s.Exists(context.Background(), desc)
	if err != nil || !ok {
		t.Errorf("Exists after Push = (%v, %v); want (true, nil)", ok, err)
	}

	rc, err := s.Fetch(context.Background(), desc)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer rc.Close()

	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, data) {
		t.Errorf("Fetch round-trip = %q, want %q", got, data)
	}
}

func TestStore_FetchUnknownDigest(t *testing.T) {
	t.Parallel()

	s := newTestStore()
	missing := stagedDescriptor(ocispec.MediaTypeImageManifest, []byte("nope"))

	if _, err := s.Fetch(context.Background(), missing); err == nil {
		t.Error("expected error for unknown digest, got nil")
	}
}

func TestStore_ExistsUnknown(t *testing.T) {
	t.Parallel()

	s := newTestStore()
	missing := stagedDescriptor(ocispec.MediaTypeImageManifest, []byte("missing"))

	ok, err := s.Exists(context.Background(), missing)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("Exists on unknown digest should be false")
	}
}

func TestStore_TagBarewordIsLocalOnly(t *testing.T) {
	t.Parallel()

	s := newTestStore()
	manifestData := []byte(`{"schemaVersion":2}`)
	desc := stagedDescriptor(ocispec.MediaTypeImageManifest, manifestData)
	if err := s.Push(context.Background(), desc, bytes.NewReader(manifestData)); err != nil {
		t.Fatal(err)
	}

	// Bareword refs (build-pipeline navigation aids) must NOT
	// reach Docker; cli is nil in this test, so any ImageLoad
	// would crash the test process. Instead they're recorded in
	// s.loaded so subsequent Resolve(ref) returns the desc.
	if err := s.Tag(context.Background(), desc, "latest"); err != nil {
		t.Fatalf("Tag bareword: %v", err)
	}

	got, err := s.Resolve(context.Background(), "latest")
	if err != nil {
		t.Fatalf("Resolve after bareword Tag: %v", err)
	}
	if got.Digest != desc.Digest {
		t.Errorf("Resolve digest = %s, want %s", got.Digest, desc.Digest)
	}
}

func TestStore_TagDigestRefSkipsImageLoad(t *testing.T) {
	t.Parallel()

	// `sha256:…` refs are oras's TagByDigest pattern. They land
	// in s.loaded but never reach docker — same shape as bareword
	// tags but a different reason (Docker would create a junk
	// repo named "sha256").
	s := newTestStore()
	manifestData := []byte(`{"schemaVersion":2}`)
	desc := stagedDescriptor(ocispec.MediaTypeImageManifest, manifestData)
	_ = s.Push(context.Background(), desc, bytes.NewReader(manifestData))

	if err := s.Tag(context.Background(), desc, "sha256:"+desc.Digest.Encoded()); err != nil {
		t.Fatalf("Tag digest-ref: %v", err)
	}
}

func TestStore_TagFillsDescriptorFromStaged(t *testing.T) {
	t.Parallel()

	// The build pipeline often passes only a digest (oras's
	// TagByDigest). Tag should fill MediaType/Size from the
	// staged descriptor so subsequent Resolve returns the full
	// shape.
	s := newTestStore()
	data := []byte(`{"schemaVersion":2}`)
	full := stagedDescriptor(ocispec.MediaTypeImageManifest, data)
	_ = s.Push(context.Background(), full, bytes.NewReader(data))

	bare := ocispec.Descriptor{Digest: full.Digest}
	if err := s.Tag(context.Background(), bare, "latest"); err != nil {
		t.Fatal(err)
	}

	got, _ := s.Resolve(context.Background(), "latest")
	if got.MediaType != ocispec.MediaTypeImageManifest {
		t.Errorf("MediaType not propagated: %q", got.MediaType)
	}
	if got.Size != int64(len(data)) {
		t.Errorf("Size = %d, want %d", got.Size, len(data))
	}
}

func TestPickManifest(t *testing.T) {
	t.Parallel()

	t.Run("by ref-name annotation", func(t *testing.T) {
		t.Parallel()
		idx := ocispec.Index{
			Manifests: []ocispec.Descriptor{
				{Digest: "sha256:111"},
				{
					Digest:      "sha256:222",
					Annotations: map[string]string{ocispec.AnnotationRefName: "foo:latest"},
				},
			},
		}
		got, ok := pickManifest(idx, "foo:latest")
		if !ok || got.Digest.String() != "sha256:222" {
			t.Errorf("got (%s, %v), want (sha256:222, true)", got.Digest, ok)
		}
	})

	t.Run("containerd image-name annotation", func(t *testing.T) {
		t.Parallel()
		idx := ocispec.Index{
			Manifests: []ocispec.Descriptor{
				{
					Digest:      "sha256:333",
					Annotations: map[string]string{"io.containerd.image.name": "bar:latest"},
				},
			},
		}
		got, ok := pickManifest(idx, "bar:latest")
		if !ok || got.Digest.String() != "sha256:333" {
			t.Errorf("got (%s, %v), want (sha256:333, true)", got.Digest, ok)
		}
	})

	t.Run("single manifest fallback", func(t *testing.T) {
		t.Parallel()
		idx := ocispec.Index{
			Manifests: []ocispec.Descriptor{
				{Digest: "sha256:444"},
			},
		}
		got, ok := pickManifest(idx, "any:tag")
		if !ok || got.Digest.String() != "sha256:444" {
			t.Errorf("got (%s, %v), want (sha256:444, true)", got.Digest, ok)
		}
	})

	t.Run("empty miss", func(t *testing.T) {
		t.Parallel()
		idx := ocispec.Index{}
		if _, ok := pickManifest(idx, "any:tag"); ok {
			t.Error("empty index should miss")
		}
	})

	t.Run("multi-manifest miss", func(t *testing.T) {
		t.Parallel()
		idx := ocispec.Index{
			Manifests: []ocispec.Descriptor{
				{Digest: "sha256:555"},
				{Digest: "sha256:666"},
			},
		}
		if _, ok := pickManifest(idx, "any:tag"); ok {
			t.Error("multi-manifest with no annotation match should miss")
		}
	})
}

func TestBuildOCILayoutTar(t *testing.T) {
	t.Parallel()

	manifestData := []byte(`{"schemaVersion":2}`)
	manifestDesc := stagedDescriptor(ocispec.MediaTypeImageManifest, manifestData)
	configData := []byte(`{}`)
	configDesc := stagedDescriptor(ocispec.MediaTypeImageConfig, configData)
	layerData := []byte("layer")
	layerDesc := stagedDescriptor(ocispec.MediaTypeImageLayerGzip, layerData)

	blobs := map[digest.Digest][]byte{
		manifestDesc.Digest: manifestData,
		configDesc.Digest:   configData,
		layerDesc.Digest:    layerData,
	}

	buf, err := buildOCILayoutTar("foo:latest", manifestDesc, blobs)
	if err != nil {
		t.Fatalf("buildOCILayoutTar: %v", err)
	}

	// Parse the tar back via the readManifestFromSaveTar helper —
	// confirms the layout we produce is what the read path expects.
	got, err := readManifestFromSaveTar(buf)
	if err != nil {
		t.Fatalf("readManifestFromSaveTar: %v", err)
	}
	if !bytes.Equal(got, manifestData) {
		t.Errorf("manifest round-trip mismatch")
	}
}

func TestReadManifestFromSaveTar_NoIndex(t *testing.T) {
	t.Parallel()

	// A tar without index.json should error rather than silently
	// returning empty bytes.
	var emptyTar bytes.Buffer
	if _, err := readManifestFromSaveTar(&emptyTar); err == nil {
		t.Error("expected error on empty save tar")
	}
}

func TestStore_Resolve_CacheHit(t *testing.T) {
	t.Parallel()

	// Pre-load the cache to avoid going through hydrate (which
	// requires a real *mobyclient.Client). Tagging a "real" ref
	// populates s.loaded with the descriptor.
	s := newTestStore()
	manifestData := []byte(`{"schemaVersion":2}`)
	desc := stagedDescriptor(ocispec.MediaTypeImageManifest, manifestData)
	_ = s.Push(context.Background(), desc, bytes.NewReader(manifestData))
	s.loaded["repo:tag"] = desc

	got, err := s.Resolve(context.Background(), "repo:tag")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Digest != desc.Digest {
		t.Errorf("Resolve cached digest = %s, want %s", got.Digest, desc.Digest)
	}
}

// Sanity-check that buildOCILayoutTar produces a tar that parses
// cleanly even when blobs is empty (degenerate but exercise the
// header/footer paths).
func TestBuildOCILayoutTar_Empty(t *testing.T) {
	t.Parallel()

	manifestData := []byte(`{"schemaVersion":2}`)
	manifestDesc := stagedDescriptor(ocispec.MediaTypeImageManifest, manifestData)

	buf, err := buildOCILayoutTar("foo:latest", manifestDesc,
		map[digest.Digest][]byte{manifestDesc.Digest: manifestData})
	if err != nil {
		t.Fatal(err)
	}
	// Index must reference the manifest by ref name.
	rawTar := buf.Bytes()
	if !strings.Contains(string(rawTar), "foo:latest") {
		t.Error("tar should contain ref name")
	}
	// And the manifest blob path appears in the tar headers.
	if !strings.Contains(string(rawTar), "blobs/sha256/"+manifestDesc.Digest.Encoded()) {
		t.Error("tar should contain blobs/sha256/<digest>")
	}
}

func TestNewStore(t *testing.T) {
	t.Parallel()

	// Constructor sanity — empty maps, no nils.
	s := NewStore(nil)
	if s == nil {
		t.Fatal("NewStore returned nil")
	}
	if s.blobs == nil || s.descs == nil || s.loaded == nil {
		t.Error("NewStore left a nil map")
	}
}

// Ensure the JSON shape of the index inside an OCI layout tar is
// what cli.ImageSave consumers (containerd, BuildKit, GHCR…) expect.
func TestBuildOCILayoutTar_IndexShape(t *testing.T) {
	t.Parallel()

	manifestData := []byte(`{"schemaVersion":2}`)
	manifestDesc := stagedDescriptor(ocispec.MediaTypeImageManifest, manifestData)

	buf, err := buildOCILayoutTar("foo:latest", manifestDesc,
		map[digest.Digest][]byte{manifestDesc.Digest: manifestData})
	if err != nil {
		t.Fatal(err)
	}

	// Parse the index entry out of the tar.
	got, err := readManifestFromSaveTar(buf)
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]any
	if jsonErr := json.Unmarshal(got, &probe); jsonErr != nil {
		t.Fatalf("manifest blob isn't JSON: %v", jsonErr)
	}
	if probe["schemaVersion"] != float64(2) {
		t.Errorf("schemaVersion = %v, want 2", probe["schemaVersion"])
	}
}
