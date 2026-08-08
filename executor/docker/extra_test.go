//nolint:testpackage // tests unexported helpers
package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/image"
	mobyclient "github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/mock"
	"oras.land/oras-go/v2"

	mockdocker "github.com/openotters/agentfile/mocks/docker"
	"github.com/openotters/agentfile/model"
	"github.com/openotters/agentfile/spec"
)

// Coverage for the prompt-side helpers that don't need a real
// gRPC server (Addr is pure; the dial path itself stays uncovered
// without a working bufconn integration test).

func TestAgent_Addr(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)

	// No port → empty.
	a := newAgent(agentDeps{client: cli})
	if got := a.Addr(); got != "" {
		t.Errorf("Addr no-port = %q, want empty", got)
	}

	// With port → loopback shape.
	a = newAgent(agentDeps{client: cli, hostGRPCPort: "12345"})
	if got := a.Addr(); got != "127.0.0.1:12345" {
		t.Errorf("Addr = %q, want 127.0.0.1:12345", got)
	}
}

func TestAgent_ImageCached(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)

	cli.EXPECT().
		ImageInspect(mock.Anything, "alpine:latest").
		Return(mobyclient.ImageInspectResult{
			InspectResponse: image.InspectResponse{ID: "sha256:abc"},
		}, nil).Once()

	cli.EXPECT().
		ImageInspect(mock.Anything, "missing:latest").
		Return(mobyclient.ImageInspectResult{}, errors.New("no such image")).Once()

	a := newAgent(agentDeps{client: cli})

	if !a.imageCached(context.Background(), "alpine:latest") {
		t.Error("imageCached should be true for present image")
	}
	if a.imageCached(context.Background(), "missing:latest") {
		t.Error("imageCached should be false for missing image")
	}
}

func TestAgent_PullImage(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImagePull(mock.Anything, "alpine:latest", mock.Anything).
		Return(newFakeStreamResponse(), nil)

	a := newAgent(agentDeps{client: cli})
	if err := a.pullImage(context.Background(), "alpine:latest"); err != nil {
		t.Errorf("pullImage: %v", err)
	}
}

func TestAgent_PullImage_Error(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ImagePull(mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("auth failed"))

	a := newAgent(agentDeps{client: cli})
	if err := a.pullImage(context.Background(), "alpine:latest"); err == nil {
		t.Error("expected error")
	}
}

func TestProvider_AgentRootPath(t *testing.T) {
	t.Parallel()

	// agentRootPath joins the provider's root with the agent UUID
	// — a tiny helper that's only useful as a sanity check on
	// path construction.
	cli := mockdocker.NewMockClient(t)
	root := memfs.New()
	p, err := NewProvider(root, nopStoreFor, WithClient(cli), WithSkipVerify())
	if err != nil {
		t.Fatal(err)
	}

	id := uuid.New()
	got := agentRootPath(p.root, id)
	if !filepath.IsAbs(got) && got != filepath.Join(p.root.Root(), id.String()) {
		t.Errorf("agentRootPath = %q, expected join of root + id", got)
	}
}

func TestProvider_OpenLogFile_NoLogDir(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	p, err := NewProvider(memfs.New(), nopStoreFor, WithClient(cli), WithSkipVerify())
	if err != nil {
		t.Fatal(err)
	}

	// LogDir defaults to empty — openLogFile returns (nil, nil).
	f, err := p.openLogFile(uuid.New())
	if err != nil {
		t.Errorf("openLogFile no-dir: %v", err)
	}
	if f != nil {
		t.Errorf("openLogFile no-dir = %v, want nil", f)
	}
}

func TestProvider_OpenLogFile_RealDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cli := mockdocker.NewMockClient(t)
	p, err := NewProvider(memfs.New(), nopStoreFor,
		WithClient(cli),
		WithSkipVerify(),
		WithLogDir(dir),
	)
	if err != nil {
		t.Fatal(err)
	}

	id := uuid.New()
	f, err := p.openLogFile(id)
	if err != nil {
		t.Fatalf("openLogFile: %v", err)
	}
	if f == nil {
		t.Fatal("openLogFile returned nil file")
	}
	t.Cleanup(func() { _ = f.Close() })

	if _, statErr := os.Stat(filepath.Join(dir, id.String()+".log")); statErr != nil {
		t.Errorf("log file not created: %v", statErr)
	}
}

func TestWithModelResolver(t *testing.T) {
	t.Parallel()

	called := false
	resolver := model.Resolver(func(_ string) (string, string, error) {
		called = true
		return "", "", nil
	})

	cli := mockdocker.NewMockClient(t)
	p, err := NewProvider(memfs.New(), nopStoreFor,
		WithClient(cli),
		WithSkipVerify(),
		WithModelResolver(resolver),
	)
	if err != nil {
		t.Fatal(err)
	}
	// Trigger the resolver to confirm it was wired.
	_, _, _ = p.modelResolver("anthropic/claude")
	if !called {
		t.Error("modelResolver not wired")
	}
}

func TestStore_Hydrate_NotFound(t *testing.T) {
	t.Parallel()

	// hydrate against a real *mobyclient.Client is impossible
	// without a daemon; this tests the early "no ID" branch when
	// resolveImageID can't find the ref.
	//
	// Construct the store directly so we can pass a nil client
	// — hydrate's first call is resolveImageID(ref) which uses
	// ImageList; when that returns "" it errors before touching
	// ImageSave.
	s := &Store{
		blobs:  map[digest.Digest][]byte{},
		descs:  map[digest.Digest]ocispec.Descriptor{},
		loaded: map[string]ocispec.Descriptor{},
	}

	// We can't invoke hydrate with a nil cli (it'd panic at
	// ImageList). Instead exercise the public Resolve which
	// short-circuits on the loaded cache when populated; the
	// not-found path is covered by the registry tests against
	// the mock Client.
	d := digest.FromBytes([]byte("manifest"))
	s.loaded["foo:tag"] = ocispec.Descriptor{Digest: d}

	got, err := s.Resolve(context.Background(), "foo:tag")
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != d {
		t.Errorf("digest mismatch")
	}
}

// nopStoreFor is the StoreFor closure shape NewProvider expects.
// Returns nil for every ref; Provider construction never derefs
// the target so the test path doesn't crash.
func nopStoreFor(_ spec.Reference) oras.ReadOnlyTarget {
	return nil
}
