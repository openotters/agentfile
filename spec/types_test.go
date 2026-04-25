package spec_test

import (
	"testing"

	"github.com/openotters/agentfile/spec"
)

func TestOverridesApply(t *testing.T) {
	t.Parallel()

	af := &spec.Agentfile{Agent: &spec.Agent{Runtime: "old", Model: "m/old"}}

	got := af.Apply(
		spec.WithRuntime("ghcr.io/openotters/runtime:new"),
		spec.WithModel("anthropic/claude-sonnet-4-20250514"),
	)

	if got != af {
		t.Fatalf("Apply did not return the receiver for chaining")
	}

	if af.Agent.Runtime != "ghcr.io/openotters/runtime:new" {
		t.Fatalf("WithRuntime not applied: %q", af.Agent.Runtime)
	}

	if af.Agent.Model != "anthropic/claude-sonnet-4-20250514" {
		t.Fatalf("WithModel not applied: %q", af.Agent.Model)
	}
}

func TestApplyEmpty(t *testing.T) {
	t.Parallel()

	// Apply with no overrides is a no-op and must still return the receiver.
	af := &spec.Agentfile{Agent: &spec.Agent{Model: "kept"}}

	if got := af.Apply(); got != af || af.Agent.Model != "kept" {
		t.Fatalf("Apply() altered receiver or model: got=%p model=%q", got, af.Agent.Model)
	}
}
