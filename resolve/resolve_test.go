package resolve_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/openotters/agentfile/resolve"
	"github.com/openotters/agentfile/spec"
)

func staticFetcher(agents map[string]*spec.Agentfile) resolve.Fetcher {
	return func(_ context.Context, ref string) (*spec.Agentfile, error) {
		af, ok := agents[ref]
		if !ok {
			return nil, fmt.Errorf("not found: %s", ref)
		}

		return af, nil
	}
}

func TestResolve_Scratch(t *testing.T) {
	t.Parallel()

	af := &spec.Agentfile{
		Agent: &spec.Agent{
			From:   "scratch",
			Name:   "test",
			Labels: map[string]string{},
			Args:   map[string]string{},
		},
	}

	result, err := resolve.Resolve(context.Background(), af, nil)
	if err != nil {
		t.Fatal(err)
	}

	if result.Agent.Name != "test" {
		t.Errorf("name = %q", result.Agent.Name)
	}
}

func TestResolve_SingleParent(t *testing.T) {
	t.Parallel()

	parent := &spec.Agentfile{
		Syntax: "openotters/agentfile:1",
		Agent: &spec.Agent{
			From:    "scratch",
			Runtime: "ghcr.io/openotters/runtime:v1",
			Model:   "anthropic/claude-haiku-4-5-20251001",
			Name:    "parent",
			Contexts: []*spec.Context{
				{Name: "SOUL", Content: "parent soul"},
			},
			Configs: []*spec.Config{
				{Key: "max-tokens", Value: "1024"},
			},
			Bins: []*spec.Bin{
				{Name: "wget", Image: "ghcr.io/openotters/tools/wget:latest"},
			},
			Labels: map[string]string{"maintainer": "parent@example.com"},
			Args:   map[string]string{},
		},
	}

	child := &spec.Agentfile{
		Agent: &spec.Agent{
			From: "registry.example.com/parent:v1",
			Name: "child",
			Contexts: []*spec.Context{
				{Name: "SOUL", Content: "child soul"},
				{Name: "IDENTITY", Content: "child identity"},
			},
			Bins: []*spec.Bin{
				{Name: "jq", Image: "ghcr.io/openotters/tools/jq:latest"},
			},
			Labels: map[string]string{"description": "child agent"},
			Args:   map[string]string{},
		},
	}

	fetch := staticFetcher(map[string]*spec.Agentfile{
		"registry.example.com/parent:v1": parent,
	})

	result, err := resolve.Resolve(context.Background(), child, fetch)
	if err != nil {
		t.Fatal(err)
	}

	a := result.Agent

	if a.Runtime != "ghcr.io/openotters/runtime:v1" {
		t.Errorf("runtime = %q, want inherited from parent", a.Runtime)
	}

	if a.Model != "anthropic/claude-haiku-4-5-20251001" {
		t.Errorf("model = %q, want inherited from parent", a.Model)
	}

	if a.Name != "child" {
		t.Errorf("name = %q, want child override", a.Name)
	}

	// SOUL overridden, IDENTITY appended
	if len(a.Contexts) != 2 {
		t.Fatalf("contexts = %d, want 2", len(a.Contexts))
	}

	if a.Contexts[0].Name != "SOUL" || a.Contexts[0].Content != "child soul" {
		t.Errorf("context SOUL = %q, want child override", a.Contexts[0].Content)
	}

	if a.Contexts[1].Name != "IDENTITY" {
		t.Errorf("context[1] = %q, want IDENTITY appended", a.Contexts[1].Name)
	}

	// Configs inherited (no RUNTIME change)
	if len(a.Configs) != 1 || a.Configs[0].Key != "max-tokens" {
		t.Errorf("configs = %v, want inherited from parent", a.Configs)
	}

	// Bins: parent + child
	if len(a.Bins) != 2 {
		t.Fatalf("bins = %d, want 2", len(a.Bins))
	}

	if a.Bins[0].Name != "wget" || a.Bins[1].Name != "jq" {
		t.Errorf("bins = [%s, %s]", a.Bins[0].Name, a.Bins[1].Name)
	}

	// Labels: merged, child wins
	if a.Labels["maintainer"] != "parent@example.com" {
		t.Errorf("label maintainer = %q, want inherited", a.Labels["maintainer"])
	}

	if a.Labels["description"] != "child agent" {
		t.Errorf("label description = %q, want child", a.Labels["description"])
	}
}

func TestResolve_RuntimeOverrideClearsParentConfigs(t *testing.T) {
	t.Parallel()

	parent := &spec.Agentfile{
		Agent: &spec.Agent{
			From:    "scratch",
			Runtime: "ghcr.io/openotters/runtime:v1",
			Configs: []*spec.Config{
				{Key: "max-tokens", Value: "1024"},
				{Key: "max-iterations", Value: "10"},
			},
			Labels: map[string]string{},
			Args:   map[string]string{},
		},
	}

	child := &spec.Agentfile{
		Agent: &spec.Agent{
			From:    "registry.example.com/parent:v1",
			Runtime: "ghcr.io/openotters/runtime:v2",
			Configs: []*spec.Config{
				{Key: "timeout", Value: "30"},
			},
			Labels: map[string]string{},
			Args:   map[string]string{},
		},
	}

	fetch := staticFetcher(map[string]*spec.Agentfile{
		"registry.example.com/parent:v1": parent,
	})

	result, err := resolve.Resolve(context.Background(), child, fetch)
	if err != nil {
		t.Fatal(err)
	}

	if result.Agent.Runtime != "ghcr.io/openotters/runtime:v2" {
		t.Errorf("runtime = %q, want v2", result.Agent.Runtime)
	}

	if len(result.Agent.Configs) != 1 {
		t.Fatalf("configs = %d, want 1 (parent configs cleared)", len(result.Agent.Configs))
	}

	if result.Agent.Configs[0].Key != "timeout" {
		t.Errorf("config[0] = %q, want timeout", result.Agent.Configs[0].Key)
	}
}

func TestResolve_RecursiveInheritance(t *testing.T) {
	t.Parallel()

	grandparent := &spec.Agentfile{
		Agent: &spec.Agent{
			From:  "scratch",
			Model: "anthropic/claude-haiku-4-5-20251001",
			Bins: []*spec.Bin{
				{Name: "wget", Image: "ghcr.io/openotters/tools/wget:latest"},
			},
			Labels: map[string]string{"org": "openotters"},
			Args:   map[string]string{},
		},
	}

	parent := &spec.Agentfile{
		Agent: &spec.Agent{
			From: "registry.example.com/grandparent:v1",
			Name: "parent",
			Bins: []*spec.Bin{
				{Name: "jq", Image: "ghcr.io/openotters/tools/jq:latest"},
			},
			Labels: map[string]string{},
			Args:   map[string]string{},
		},
	}

	child := &spec.Agentfile{
		Agent: &spec.Agent{
			From: "registry.example.com/parent:v1",
			Name: "child",
			Bins: []*spec.Bin{
				{Name: "cat", Image: "ghcr.io/openotters/tools/cat:latest"},
			},
			Labels: map[string]string{},
			Args:   map[string]string{},
		},
	}

	fetch := staticFetcher(map[string]*spec.Agentfile{
		"registry.example.com/grandparent:v1": grandparent,
		"registry.example.com/parent:v1":      parent,
	})

	result, err := resolve.Resolve(context.Background(), child, fetch)
	if err != nil {
		t.Fatal(err)
	}

	if result.Agent.Name != "child" {
		t.Errorf("name = %q", result.Agent.Name)
	}

	if result.Agent.Model != "anthropic/claude-haiku-4-5-20251001" {
		t.Errorf("model = %q, want from grandparent", result.Agent.Model)
	}

	if len(result.Agent.Bins) != 3 {
		t.Fatalf("bins = %d, want 3 (wget+jq+cat)", len(result.Agent.Bins))
	}

	if result.Agent.Labels["org"] != "openotters" {
		t.Errorf("label org = %q, want from grandparent", result.Agent.Labels["org"])
	}
}

func TestResolve_DepthLimit(t *testing.T) {
	t.Parallel()

	circular := &spec.Agentfile{
		Agent: &spec.Agent{
			From:   "registry.example.com/self:v1",
			Labels: map[string]string{},
			Args:   map[string]string{},
		},
	}

	fetch := staticFetcher(map[string]*spec.Agentfile{
		"registry.example.com/self:v1": circular,
	})

	_, err := resolve.Resolve(context.Background(), circular, fetch)
	if err == nil {
		t.Fatal("expected error for circular reference")
	}
}

func TestResolve_ParentNotFound(t *testing.T) {
	t.Parallel()

	child := &spec.Agentfile{
		Agent: &spec.Agent{
			From:   "registry.example.com/missing:v1",
			Labels: map[string]string{},
			Args:   map[string]string{},
		},
	}

	fetch := staticFetcher(map[string]*spec.Agentfile{})

	_, err := resolve.Resolve(context.Background(), child, fetch)
	if err == nil {
		t.Fatal("expected error for missing parent")
	}
}
