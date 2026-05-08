//nolint:testpackage // tests unexported helpers
package system

import (
	"context"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/google/uuid"
	"oras.land/oras-go/v2"

	"github.com/openotters/agentfile/spec"
)

// Destroy on an empty root is a no-op; covers the early "no
// agent dirs" branch without needing a real puller.
func TestProvider_Destroy_EmptyRoot(t *testing.T) {
	t.Parallel()

	root := memfs.New()
	p := NewProvider(root, func(_ spec.Reference) oras.ReadOnlyTarget { return nil })

	if err := p.Destroy(context.Background()); err != nil {
		t.Errorf("Destroy on empty root: %v", err)
	}
}

func TestProvider_Load_Empty(t *testing.T) {
	t.Parallel()

	root := memfs.New()
	p := NewProvider(root, func(_ spec.Reference) oras.ReadOnlyTarget { return nil })

	got, err := p.Load(context.Background())
	if err != nil {
		t.Errorf("Load: %v", err)
	}
	if got != nil {
		t.Errorf("Load on empty root = %v, want nil", got)
	}
}

func TestProvider_Create(t *testing.T) {
	t.Parallel()

	root := memfs.New()
	storeFor := func(_ spec.Reference) oras.ReadOnlyTarget { return nil }

	p := NewProvider(root, storeFor)

	ref := spec.Reference{Name: "foo", Tag: "latest"}
	a, err := p.Create(context.Background(), uuid.New(), ref)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if a == nil {
		t.Fatal("nil agent")
	}
	// Created agent should be a *system.Agent (the concrete
	// type CreateWithOptions returns).
	if _, ok := a.(*Agent); !ok {
		t.Errorf("Create returned %T, want *system.Agent", a)
	}
}

func TestProvider_CreateWithOptions(t *testing.T) {
	t.Parallel()

	root := memfs.New()
	storeFor := func(_ spec.Reference) oras.ReadOnlyTarget { return nil }
	logDir := t.TempDir()

	p := NewProvider(root, storeFor, WithLogDir(logDir))

	ref := spec.Reference{Name: "foo", Tag: "latest"}
	a, err := p.CreateWithOptions(context.Background(), uuid.New(), ref, []AgentOption{
		// no extras — just exercise the path that takes the
		// per-instance slice.
	})
	if err != nil {
		t.Fatalf("CreateWithOptions: %v", err)
	}
	if a == nil {
		t.Fatal("nil agent")
	}
}

func TestProvider_RegistryWiring(t *testing.T) {
	t.Parallel()

	root := memfs.New()
	p := NewProvider(root, func(_ spec.Reference) oras.ReadOnlyTarget { return nil },
		WithRegistryAddr("host:port"))

	r1 := p.Registry()
	r2 := p.Registry()
	if r1 != r2 {
		t.Error("Registry should be cached on the Provider")
	}
}
