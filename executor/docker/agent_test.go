//nolint:testpackage // tests unexported helpers
package docker

import (
	"context"
	"testing"

	"github.com/google/uuid"
	mobyclient "github.com/moby/moby/client"
	"github.com/stretchr/testify/mock"

	"github.com/openotters/agentfile/executor"
	mockdocker "github.com/openotters/agentfile/mocks/docker"
)

func TestSplitProviderPrefix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in       string
		provider string
		model    string
	}{
		{"anthropic/claude-haiku-4-5", "anthropic", "claude-haiku-4-5"},
		{"openai/gpt-5", "openai", "gpt-5"},
		{"no-slash", "", "no-slash"},
		{"/leading", "", "/leading"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			t.Parallel()
			p, m := splitProviderPrefix(c.in)
			if p != c.provider || m != c.model {
				t.Errorf("(%q, %q), want (%q, %q)", p, m, c.provider, c.model)
			}
		})
	}
}

func TestNewAgent_BasicGetters(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	cli := mockdocker.NewMockClient(t)

	a := newAgent(agentDeps{
		id:           id,
		client:       cli,
		hostGRPCPort: "65432",
	})

	if a.UUID() != id {
		t.Errorf("UUID = %v, want %v", a.UUID(), id)
	}
	if a.Status().String() == "" {
		t.Errorf("Status should be initialised")
	}
	if a.Runtime() != nil {
		t.Errorf("Runtime should be nil before Prepare")
	}
}

func TestAgent_StopWithoutContainer(t *testing.T) {
	t.Parallel()

	// Stop on an agent that never started a container is a no-op
	// — the early return path that we want covered.
	cli := mockdocker.NewMockClient(t)
	a := newAgent(agentDeps{client: cli})

	if err := a.Stop(context.Background()); err != nil {
		t.Errorf("Stop on never-started: %v", err)
	}
}

func TestAgent_RanChannelHelpers(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	a := newAgent(agentDeps{client: cli})

	if got := a.getRan(); got != nil {
		t.Errorf("getRan before setRan should be nil, got %v", got)
	}

	ch := make(chan struct{})
	a.setRan(ch)

	if got := a.getRan(); got != ch {
		t.Errorf("getRan after setRan returned wrong channel")
	}
}

func TestAgent_IDLocked(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	a := newAgent(agentDeps{client: cli})

	if got := a.idLocked(); got != "" {
		t.Errorf("idLocked before create = %q, want empty", got)
	}

	a.containerID = "abc123"
	if got := a.idLocked(); got != "abc123" {
		t.Errorf("idLocked after assign = %q, want abc123", got)
	}
}

func TestAgent_RuntimeRef(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	a := newAgent(agentDeps{client: cli})

	// No runtime → empty.
	if got := a.runtimeRef(&executor.Runtime{}); got != "" {
		t.Errorf("runtimeRef no-source = %q, want empty", got)
	}

	// Provenance wins when set.
	rt := &executor.Runtime{
		ResolvedConfig: executor.ResolvedConfig{
			Provenance: &executor.Provenance{RuntimeRef: "ghcr.io/openotters/runtime:v1"},
		},
	}
	if got := a.runtimeRef(rt); got != "ghcr.io/openotters/runtime:v1" {
		t.Errorf("runtimeRef provenance = %q", got)
	}
}

func TestAgent_SubscribeStatus(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	a := newAgent(agentDeps{client: cli})

	ch, cancel := a.SubscribeStatus()
	if ch == nil {
		t.Error("SubscribeStatus returned nil channel")
	}
	if cancel == nil {
		t.Error("SubscribeStatus returned nil cancel")
	}

	cancel()
}

func TestAgent_StopWithContainer(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ContainerStop(mock.Anything, "abc123", mock.Anything).
		Return(mobyclient.ContainerStopResult{}, nil)

	a := newAgent(agentDeps{client: cli})
	a.containerID = "abc123"

	// Pre-close the ran channel so Stop returns immediately
	// after ContainerStop without blocking on a real run loop.
	ch := make(chan struct{})
	close(ch)
	a.setRan(ch)

	if err := a.Stop(context.Background()); err != nil {
		t.Errorf("Stop with container: %v", err)
	}
}

func TestAgent_RemoveWithContainer(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	cli.EXPECT().
		ContainerRemove(mock.Anything, "abc123", mock.Anything).
		Return(mobyclient.ContainerRemoveResult{}, nil)

	a := newAgent(agentDeps{client: cli})
	a.containerID = "abc123"

	if err := a.Remove(context.Background()); err != nil {
		t.Errorf("Remove with container: %v", err)
	}
	if a.idLocked() != "" {
		t.Error("containerID should be cleared after Remove")
	}
}

func TestAgent_RemoveNoContainer(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	a := newAgent(agentDeps{client: cli})

	// Remove on a never-started agent shouldn't reach the
	// daemon (no ImageRemove expectations). Status transitions
	// through Removing → Removed.
	if err := a.Remove(context.Background()); err != nil {
		t.Errorf("Remove no-container: %v", err)
	}

	if got := a.Status(); got != executor.StatusRemoved {
		t.Errorf("post-Remove status = %v, want StatusRemoved", got)
	}
}
