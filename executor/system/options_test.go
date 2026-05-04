//nolint:testpackage // direct internal access
package system

import (
	"context"
	"io"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/google/uuid"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"

	agentoci "github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
)

// --- ProviderOption coverage --------------------------------------------

func newTestProvider(opts ...ProviderOption) *Provider {
	storeFor := func(spec.Reference) oras.ReadOnlyTarget { return memory.New() }
	return NewProvider(memfs.New(), storeFor, opts...)
}

func TestWithPuller(t *testing.T) {
	t.Parallel()

	called := false
	stub := agentoci.Puller(func(context.Context, spec.Reference, io.Writer) error {
		called = true
		return nil
	})

	p := newTestProvider(WithPuller(stub))

	// Invoke the stored puller — identity via observable side effect is
	// enough; we don't want to rely on pointer equality of func values.
	_ = p.ociPuller(context.Background(), spec.Reference{}, io.Discard)

	if !called {
		t.Fatal("WithPuller did not install the provided puller")
	}
}

func TestWithLocalRuntime(t *testing.T) {
	t.Parallel()

	p := newTestProvider(WithLocalRuntime("/tmp/runtime"))

	if p.localRuntime != "/tmp/runtime" {
		t.Fatalf("WithLocalRuntime not applied: %q", p.localRuntime)
	}
}

func TestWithAgentDefaults(t *testing.T) {
	t.Parallel()

	// Agent defaults are applied when Provider builds AgentOptions.
	// Verify by checking the slice length; the identity of each option
	// is difficult to assert without running Create.
	p := newTestProvider(WithAgentDefaults(
		WithAddr("127.0.0.1:1000"),
		WithStdout(nil),
	))

	if len(p.agentDefaults) != 2 {
		t.Fatalf("agentDefaults len = %d, want 2", len(p.agentDefaults))
	}
}

func TestWithLogDir(t *testing.T) {
	t.Parallel()

	p := newTestProvider(WithLogDir("/var/log/otters"))

	if p.logDir != "/var/log/otters" {
		t.Fatalf("WithLogDir not applied: %q", p.logDir)
	}
}

// --- AgentOption coverage (the ones not hit by TestNewAgent_OptionsApply) --

func newTestAgent(opts ...AgentOption) *Agent {
	return NewAgent(uuid.New(), memfs.New(), opts...)
}

func TestWithStore(t *testing.T) {
	t.Parallel()

	store := memory.New()
	a := newTestAgent(WithStore(store))

	if a.ws.store != store {
		t.Fatal("WithStore did not set ws.store")
	}
}

func TestWithReference(t *testing.T) {
	t.Parallel()

	ref := spec.Reference{Name: "meteo", Tag: "v1"}
	a := newTestAgent(WithReference(ref))

	if a.ws.ref != ref {
		t.Fatalf("WithReference not applied: %+v", a.ws.ref)
	}
}

func TestWithOverrides(t *testing.T) {
	t.Parallel()

	a := newTestAgent(WithOverrides(spec.WithModel("anthropic/claude-haiku-4-5")))

	if len(a.ws.overrides) != 1 {
		t.Fatalf("overrides len = %d, want 1", len(a.ws.overrides))
	}
}

func TestWithAgentPuller(t *testing.T) {
	t.Parallel()

	called := false
	stub := agentoci.Puller(func(context.Context, spec.Reference, io.Writer) error {
		called = true
		return nil
	})

	a := newTestAgent(WithAgentPuller(stub))
	_ = a.ws.ociPuller(context.Background(), spec.Reference{}, io.Discard)

	if !called {
		t.Fatal("WithAgentPuller did not install the provided puller")
	}
}

func TestWithAgentLocalRuntime(t *testing.T) {
	t.Parallel()

	a := newTestAgent(WithAgentLocalRuntime("/tmp/runtime-bin"))

	if a.ws.localRuntime != "/tmp/runtime-bin" {
		t.Fatalf("WithAgentLocalRuntime not applied: %q", a.ws.localRuntime)
	}
}

func TestWithModelResolver(t *testing.T) {
	t.Parallel()

	resolver := func(string) (string, string, error) {
		return "https://api.example.com", "key", nil
	}

	a := newTestAgent(WithModelResolver(resolver))

	if a.ws.modelResolver == nil {
		t.Fatal("WithModelResolver did not set ws.modelResolver")
	}

	url, key, err := a.ws.modelResolver("anything")
	if err != nil || url != "https://api.example.com" || key != "key" {
		t.Fatalf("resolver returned unexpected values: url=%q key=%q err=%v", url, key, err)
	}
}

func TestWithStaticModelResolver(t *testing.T) {
	t.Parallel()

	a := newTestAgent(WithStaticModelResolver("https://api.example.com", "secret"))

	url, key, err := a.ws.modelResolver("m")
	if err != nil {
		t.Fatalf("err = %v", err)
	}

	if url != "https://api.example.com" || key != "secret" {
		t.Fatalf("static resolver returned url=%q key=%q", url, key)
	}
}
