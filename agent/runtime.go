package agent

import (
	"fmt"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/util"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/openotters/agentfile/spec"
)

const runtimeFile = "etc/agent.yaml"

// AgentRuntime is the persistable result of creating an agent.
// It contains the original spec (source of truth) and the resolved configuration.
//
//nolint:revive // public API name; renaming to Runtime is a breaking change for downstream consumers
type AgentRuntime struct {
	ID             uuid.UUID       `yaml:"id" json:"id"`
	Source         *spec.Agentfile `yaml:"source" json:"source"`
	ResolvedConfig `yaml:",inline" json:",inline"`
}

// ResolvedConfig holds the merged configuration after applying spec overrides.
type ResolvedConfig struct {
	Name    string         `yaml:"name" json:"name"`
	Model   string         `yaml:"model" json:"model"`
	Addr    string         `yaml:"addr,omitempty" json:"addr,omitempty"`
	APIBase string         `yaml:"api_base,omitempty" json:"api_base,omitempty"`
	APIKey  string         `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	Exec    []string       `yaml:"exec,omitempty" json:"exec,omitempty"`
	Tools   []ResolvedTool `yaml:"tools,omitempty" json:"tools,omitempty"`
}

// ResolvedTool describes a tool binary with its resolved filesystem path.
type ResolvedTool struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Binary      string `yaml:"binary" json:"binary"`
}

// WriteTo serializes the runtime to the given filesystem as YAML.
//
// Source's nested types (spec.Agentfile, spec.Agent, …) carry json+yaml
// tags via the spec package; the linter's musttag check only inspects
// the top-level concrete type and misses the transitive coverage.
//
//nolint:musttag // tags live on the embedded spec.* types; see comment
func (rt *AgentRuntime) WriteTo(fs billy.Filesystem) error {
	data, err := yaml.Marshal(rt)
	if err != nil {
		return fmt.Errorf("marshaling runtime: %w", err)
	}

	return util.WriteFile(fs, runtimeFile, data, 0o644)
}

// LoadRuntime reads an AgentRuntime from the given filesystem.
//
//nolint:musttag // tags live on the embedded spec.* types; see WriteTo
func LoadRuntime(fs billy.Filesystem) (*AgentRuntime, error) {
	data, err := util.ReadFile(fs, runtimeFile)
	if err != nil {
		return nil, fmt.Errorf("reading runtime: %w", err)
	}

	var rt AgentRuntime
	if e := yaml.Unmarshal(data, &rt); e != nil {
		return nil, fmt.Errorf("parsing runtime: %w", e)
	}

	return &rt, nil
}
