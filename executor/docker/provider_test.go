//nolint:testpackage // tests unexported helpers
package docker

import (
	"context"
	"strconv"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/google/uuid"
	"oras.land/oras-go/v2"

	"github.com/openotters/agentfile/executor"
	mockdocker "github.com/openotters/agentfile/mocks/docker"
	"github.com/openotters/agentfile/spec"
)

func TestNewProvider_SkipVerify(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	root := memfs.New()
	storeFor := func(_ spec.Reference) oras.ReadOnlyTarget { return nil }

	p, err := NewProvider(root, storeFor,
		WithClient(cli),
		WithSkipVerify(),
		WithBaseImage("alpine:latest"),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if p == nil {
		t.Fatal("nil provider")
	}
	if p.baseImage != "alpine:latest" {
		t.Errorf("baseImage = %q", p.baseImage)
	}
}

func TestProvider_Registry(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	root := memfs.New()
	storeFor := func(_ spec.Reference) oras.ReadOnlyTarget { return nil }

	p, err := NewProvider(root, storeFor, WithClient(cli), WithSkipVerify())
	if err != nil {
		t.Fatal(err)
	}

	r1 := p.Registry()
	r2 := p.Registry()
	// Constructed lazily — second call must hit the cached instance.
	if r1 != r2 {
		t.Error("Registry() should return the cached instance")
	}
}

func TestProvider_Load(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().Close().Return(nil).Maybe()

	root := memfs.New()
	storeFor := func(_ spec.Reference) oras.ReadOnlyTarget { return nil }

	p, err := NewProvider(root, storeFor, WithClient(cli), WithSkipVerify())
	if err != nil {
		t.Fatal(err)
	}

	// Load is currently a no-op (re-binding running containers
	// to in-process Agent values is a follow-up). Verify it
	// returns nil/nil and doesn't crash.
	got, err := p.Load(context.Background())
	if err != nil {
		t.Errorf("Load: %v", err)
	}
	if got != nil {
		t.Errorf("Load = %v, want nil", got)
	}
}

func TestProvider_Destroy_EmptyRoot(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	root := memfs.New()
	storeFor := func(_ spec.Reference) oras.ReadOnlyTarget { return nil }

	p, err := NewProvider(root, storeFor, WithClient(cli), WithSkipVerify())
	if err != nil {
		t.Fatal(err)
	}

	if dErr := p.Destroy(context.Background()); dErr != nil {
		t.Errorf("Destroy on empty root: %v", dErr)
	}
}

func TestProvider_Close(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().Close().Return(nil)

	root := memfs.New()
	storeFor := func(_ spec.Reference) oras.ReadOnlyTarget { return nil }

	p, err := NewProvider(root, storeFor, WithClient(cli), WithSkipVerify())
	if err != nil {
		t.Fatal(err)
	}

	if cErr := p.Close(); cErr != nil {
		t.Errorf("Close: %v", cErr)
	}
}

func TestProvider_Create(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	root := memfs.New()
	called := 0
	storeFor := func(_ spec.Reference) oras.ReadOnlyTarget {
		called++
		return nil
	}

	p, err := NewProvider(root, storeFor, WithClient(cli), WithSkipVerify())
	if err != nil {
		t.Fatal(err)
	}

	id := uuid.New()
	ref := spec.Reference{Name: "foo", Tag: "latest"}

	a, err := p.Create(context.Background(), id, ref)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if a == nil {
		t.Fatal("Create returned nil agent")
	}

	dockerAgent, ok := a.(*Agent)
	if !ok {
		t.Fatalf("Create returned %T, want *docker.Agent", a)
	}
	if dockerAgent.UUID() != id {
		t.Errorf("agent UUID = %v, want %v", dockerAgent.UUID(), id)
	}
	if called == 0 {
		t.Error("storeFor never called")
	}
}

func TestReserveLoopbackPort(t *testing.T) {
	t.Parallel()

	port, err := reserveLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	if port == "" {
		t.Fatal("empty port")
	}
	if _, perr := strconv.Atoi(port); perr != nil {
		t.Errorf("port %q is not numeric: %v", port, perr)
	}
}

func TestProviderOptions(t *testing.T) {
	t.Parallel()

	p := &Provider{}

	WithLogDir("/tmp/logs")(p)
	if p.logDir != "/tmp/logs" {
		t.Errorf("logDir = %q", p.logDir)
	}

	mounts := []executor.Mount{{Host: "/h", Target: "/t"}}
	WithMounts(mounts)(p)
	if len(p.mounts) != 1 || p.mounts[0].Host != "/h" {
		t.Errorf("mounts = %v", p.mounts)
	}

	cli := mockdocker.NewMockClient(t)
	WithClient(cli)(p)
	if p.client != cli {
		t.Error("WithClient didn't set client")
	}

	WithBaseImage("alpine")(p)
	if p.baseImage != "alpine" {
		t.Errorf("baseImage = %q", p.baseImage)
	}

	WithSkipVerify()(p)
	if !p.skipVerify {
		t.Error("WithSkipVerify didn't set flag")
	}
}
