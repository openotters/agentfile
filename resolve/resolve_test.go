package resolve_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/openotters/agentfile/resolve"
	"github.com/openotters/agentfile/spec"
)

func staticFetcher(agents map[string]*spec.Agentfile) resolve.Fetcher {
	return func(_ context.Context, ref spec.Reference) (*spec.Agentfile, error) {
		af, ok := agents[ref.String()]
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

func TestResolve_ExecAndEnvInheritance(t *testing.T) {
	t.Parallel()

	parent := &spec.Agentfile{Agent: &spec.Agent{
		From: "scratch",
		Exec: []string{"serve", "--parent"},
		Envs: []*spec.Env{
			{Key: "LOG_LEVEL", Value: "info"},
			{Key: "NODE_ENV", Value: "production"},
		},
	}}

	fetch := staticFetcher(map[string]*spec.Agentfile{"ghcr.io/openotters/base:v1": parent})

	t.Run("child without EXEC/ENV inherits parent's", func(t *testing.T) {
		t.Parallel()

		child := &spec.Agentfile{Agent: &spec.Agent{From: "ghcr.io/openotters/base:v1"}}

		got, err := resolve.Resolve(context.Background(), child, fetch)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}

		if len(got.Agent.Exec) != 2 || got.Agent.Exec[1] != "--parent" {
			t.Errorf("exec = %v, want parent's [serve --parent]", got.Agent.Exec)
		}

		if len(got.Agent.Envs) != 2 {
			t.Errorf("envs = %d entries, want parent's 2", len(got.Agent.Envs))
		}
	})

	t.Run("child EXEC replaces, child ENV appends", func(t *testing.T) {
		t.Parallel()

		child := &spec.Agentfile{Agent: &spec.Agent{
			From: "ghcr.io/openotters/base:v1",
			Exec: []string{"serve", "--child"},
			Envs: []*spec.Env{{Key: "LOG_LEVEL", Value: "debug"}},
		}}

		got, err := resolve.Resolve(context.Background(), child, fetch)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}

		if len(got.Agent.Exec) != 2 || got.Agent.Exec[1] != "--child" {
			t.Errorf("exec = %v, want child's [serve --child]", got.Agent.Exec)
		}

		// Append at the spec level: parent's two + child's one, child last
		// so the spawn-time keyed-merge makes LOG_LEVEL=debug win.
		if len(got.Agent.Envs) != 3 {
			t.Fatalf("envs = %d entries, want 3 (append)", len(got.Agent.Envs))
		}

		last := got.Agent.Envs[2]
		if last.Key != "LOG_LEVEL" || last.Value != "debug" {
			t.Errorf("last env = %s=%s, want child's LOG_LEVEL=debug", last.Key, last.Value)
		}
	})
}

func TestResolve_InheritedRequiredConfigSatisfiedByChild(t *testing.T) {
	t.Parallel()

	parent := &spec.Agentfile{Agent: &spec.Agent{
		From:    "scratch",
		Configs: []*spec.Config{{Key: "webhook-url", Required: true}},
	}}
	child := &spec.Agentfile{Agent: &spec.Agent{
		From:    "ghcr.io/openotters/base:v1",
		Configs: []*spec.Config{{Key: "webhook-url", Value: "https://example.com"}},
	}}

	fetch := staticFetcher(map[string]*spec.Agentfile{"ghcr.io/openotters/base:v1": parent})

	got, err := resolve.Resolve(context.Background(), child, fetch)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if reqErr := spec.RequiredConfigsProvided(got.Agent.Configs); reqErr != nil {
		t.Errorf("child value should satisfy parent's required config: %v", reqErr)
	}
}

func TestResolve_Cycle(t *testing.T) {
	t.Parallel()

	// a inherits from b, b inherits from a — the chain loops.
	a := &spec.Agentfile{Agent: &spec.Agent{From: "registry.local/agents/b:latest"}}
	b := &spec.Agentfile{Agent: &spec.Agent{From: "registry.local/agents/a:latest"}}

	fetch := staticFetcher(map[string]*spec.Agentfile{
		"registry.local/agents/a:latest": a,
		"registry.local/agents/b:latest": b,
	})

	start := &spec.Agentfile{Agent: &spec.Agent{From: "registry.local/agents/a:latest"}}

	_, err := resolve.Resolve(context.Background(), start, fetch)
	if err == nil || !strings.Contains(err.Error(), "inheritance cycle") {
		t.Fatalf("expected cycle error, got %v", err)
	}
}
