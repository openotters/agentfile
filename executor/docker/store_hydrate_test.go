//nolint:testpackage // tests unexported helpers
package docker

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/moby/moby/api/types/image"
	mobyclient "github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/mock"

	mockdocker "github.com/openotters/agentfile/mocks/docker"
)

// hydrate fetches an image out of the daemon via cli.ImageSave
// and unpacks the OCI layout tar into the store's blob cache.
// Driven through the mock Client so we don't need a real daemon.
func TestStore_Hydrate(t *testing.T) {
	t.Parallel()

	const (
		ref = "foo:latest"
		id  = "sha256:abc123"
	)

	manifestData := []byte(`{"schemaVersion":2}`)
	manifestDigest := digest.FromBytes(manifestData)

	saveTarReader, _ := makeOCILayoutTarBytesForStore(ref, ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    manifestDigest,
		Size:      int64(len(manifestData)),
	}, map[digest.Digest][]byte{
		manifestDigest: manifestData,
	})

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageList(mock.Anything, mock.Anything).
		Return(mobyclient.ImageListResult{
			Items: []image.Summary{{ID: id, RepoTags: []string{ref}}},
		}, nil)
	cli.EXPECT().
		ImageSave(mock.Anything, []string{id}, mock.Anything).
		Return(io.NopCloser(saveTarReader), nil)

	s := NewStore(cli)

	desc, err := s.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatalf("Resolve via hydrate: %v", err)
	}
	if desc.Digest != manifestDigest {
		t.Errorf("digest = %s, want %s", desc.Digest, manifestDigest)
	}

	// After hydrate, Fetch hits the local cache without calling
	// the daemon again.
	rc, err := s.Fetch(context.Background(), ocispec.Descriptor{Digest: manifestDigest})
	if err != nil {
		t.Fatalf("Fetch post-hydrate: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != string(manifestData) {
		t.Errorf("Fetch round-trip mismatch")
	}
}

func TestStore_Hydrate_NotFoundInDaemon(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageList(mock.Anything, mock.Anything).
		Return(mobyclient.ImageListResult{Items: []image.Summary{}}, nil)

	s := NewStore(cli)
	if _, err := s.Resolve(context.Background(), "missing:latest"); err == nil {
		t.Error("expected error for missing ref")
	}
}

func TestStore_Hydrate_SaveError(t *testing.T) {
	t.Parallel()

	const id = "sha256:abc"
	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageList(mock.Anything, mock.Anything).
		Return(mobyclient.ImageListResult{
			Items: []image.Summary{{ID: id, RepoTags: []string{"foo:latest"}}},
		}, nil)
	cli.EXPECT().
		ImageSave(mock.Anything, []string{id}, mock.Anything).
		Return(nil, errors.New("daemon down"))

	s := NewStore(cli)
	if _, err := s.Resolve(context.Background(), "foo:latest"); err == nil {
		t.Error("expected error from ImageSave failure")
	}
}

// makeOCILayoutTarBytesForStore is a copy of the registry-test
// helper (registry_test.go can't be imported here). Same shape:
// a minimal OCI image layout (oci-layout + index.json + one
// manifest blob) that readManifestFromSaveTar can parse.
func makeOCILayoutTarBytesForStore(
	ref string, manifestDesc ocispec.Descriptor, blobs map[digest.Digest][]byte,
) (io.Reader, error) {
	buf, err := buildOCILayoutTar(ref, manifestDesc, blobs)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

// Cover Tag's actual Docker-side ImageLoad path: when the ref
// has a colon and isn't a digest, the Store builds an OCI layout
// tar and pipes it to cli.ImageLoad.
func TestStore_Tag_ImageLoad(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageLoad(mock.Anything, mock.Anything).
		Return(io.NopCloser(emptyReader{}), nil)

	s := NewStore(cli)

	manifestData := []byte(`{"schemaVersion":2}`)
	desc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    digest.FromBytes(manifestData),
		Size:      int64(len(manifestData)),
	}
	if err := s.Push(context.Background(), desc, ioReader(manifestData)); err != nil {
		t.Fatal(err)
	}

	if err := s.Tag(context.Background(), desc, "foo:latest"); err != nil {
		t.Fatalf("Tag with image-load: %v", err)
	}
}

func TestStore_Tag_UnstagedManifest(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	s := NewStore(cli)

	// Tag with a digest the Store has never seen — should error
	// before reaching ImageLoad.
	desc := ocispec.Descriptor{Digest: digest.FromBytes([]byte("missing"))}
	if err := s.Tag(context.Background(), desc, "foo:latest"); err == nil {
		t.Error("expected error for unstaged manifest")
	}
}

func TestStore_Tag_ImageLoadError(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImageLoad(mock.Anything, mock.Anything).
		Return(nil, errors.New("docker is upset"))

	s := NewStore(cli)
	manifestData := []byte(`{"schemaVersion":2}`)
	desc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    digest.FromBytes(manifestData),
		Size:      int64(len(manifestData)),
	}
	_ = s.Push(context.Background(), desc, ioReader(manifestData))

	if err := s.Tag(context.Background(), desc, "foo:latest"); err == nil {
		t.Error("expected error when ImageLoad fails")
	}
}

// Tiny helpers so the test stays self-contained without colliding
// with names used elsewhere in the test suite.
type emptyReader struct{}

func (emptyReader) Read(_ []byte) (int, error) { return 0, io.EOF }

type bytesReader struct {
	data []byte
	pos  int
}

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func ioReader(b []byte) io.Reader { return &bytesReader{data: b} }
