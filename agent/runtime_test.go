package agent_test

import (
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/util"
	"github.com/google/uuid"

	"github.com/openotters/agentfile/agent"
	"github.com/openotters/agentfile/spec"
)

func newRT(t *testing.T) *agent.AgentRuntime {
	t.Helper()

	return &agent.AgentRuntime{
		ID:     uuid.New(),
		Source: &spec.Agentfile{Syntax: spec.DefaultSyntax, Agent: &spec.Agent{Name: "x"}},
		ResolvedConfig: agent.ResolvedConfig{
			Name:    "x",
			Model:   "anthropic/claude",
			APIBase: "https://api.example.com",
			APIKey:  "secret-do-not-leak",
		},
	}
}

// TestAgentRuntime_WriteToOmitsCredentials pins the security contract:
// neither the API key nor the API base ever land in agent.yaml. They
// travel only via the resolver + subprocess env channel.
func TestAgentRuntime_WriteToOmitsCredentials(t *testing.T) {
	t.Parallel()

	rt := newRT(t)

	fs := memfs.New()
	if err := rt.WriteTo(fs); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}

	data, err := util.ReadFile(fs, "etc/agent.yaml")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	body := string(data)
	for _, leak := range []string{"secret-do-not-leak", "api.example.com", "api_key", "api_base"} {
		if strings.Contains(body, leak) {
			t.Errorf("agent.yaml leaks %q\n---\n%s", leak, body)
		}
	}
}

// TestAgentRuntime_LoadIgnoresStaleCredentials confirms older on-disk
// agent.yaml files written by previous daemons (which DID persist
// these fields) are tolerated: the values get silently dropped on
// load. yaml:"-" means "neither marshal nor unmarshal," so present
// fields are skipped without erroring.
//
// Constructed by serialising a current-shape rt and appending the
// legacy fields manually — avoids hand-coding a full Agentfile YAML
// fixture that would drift as the spec evolves.
func TestAgentRuntime_LoadIgnoresStaleCredentials(t *testing.T) {
	t.Parallel()

	rt := newRT(t)
	fs := memfs.New()

	if err := rt.WriteTo(fs); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}

	clean, err := util.ReadFile(fs, "etc/agent.yaml")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	stale := make([]byte, 0, len(clean)+64)
	stale = append(stale, clean...)
	stale = append(stale, []byte("api_base: https://leaked.example.com\napi_key: leaked-secret\n")...)

	if writeErr := util.WriteFile(fs, "etc/agent.yaml", stale, 0o644); writeErr != nil {
		t.Fatalf("WriteFile: %v", writeErr)
	}

	loaded, err := agent.LoadRuntime(fs)
	if err != nil {
		t.Fatalf("LoadRuntime: %v", err)
	}

	if loaded.APIBase != "" || loaded.APIKey != "" {
		t.Fatalf("LoadRuntime preserved stale creds: APIBase=%q APIKey=%q",
			loaded.APIBase, loaded.APIKey)
	}
}
