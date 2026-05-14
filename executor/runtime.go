package executor

import (
	"fmt"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/util"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/openotters/agentfile/spec"
)

const runtimeFile = "etc/agent.yaml"

// Runtime is the persistable result of creating an agent. ResolvedConfig
// is the slim, single-job view that agent.yaml carries on disk. Source
// is in-memory only — the original Agentfile lives next door at
// etc/Agentfile (see spec.AgentfileMediaType) and the daemon
// re-parses it on Restore / Start when it needs the full spec.
type Runtime struct {
	Source         *spec.Agentfile `yaml:"-" json:"-"`
	ResolvedConfig `yaml:",inline" json:",inline"`
}

// ResolvedConfig is the runtime-side configuration that agent.yaml
// serialises: identity, OCI provenance (image + runtime as
// ref+digest pairs), spec-declared envs/mounts (operator values
// live in daemon.db and hydrate in-memory), declared context
// filenames, runtime tunables, and resolved tool surface. APIBase /
// APIKey are intentionally non-serialised — credentials travel via
// spawn env (<PROVIDER>_API_KEY / <PROVIDER>_API_BASE), not disk.
type ResolvedConfig struct {
	ID        uuid.UUID         `yaml:"id" json:"id"`
	Name      string            `yaml:"name" json:"name"`
	Model     string            `yaml:"model" json:"model"`
	Workspace string            `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Image     *OCIRef           `yaml:"image,omitempty" json:"image,omitempty"`
	Runtime   *OCIRef           `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Configs   map[string]string `yaml:"configs,omitempty" json:"configs,omitempty"`
	Envs      []*spec.Env       `yaml:"envs,omitempty" json:"envs,omitempty"`
	Mounts    []*spec.Mount     `yaml:"mounts,omitempty" json:"mounts,omitempty"`
	Context   []string          `yaml:"context,omitempty" json:"context,omitempty"`
	Tools     []ResolvedTool    `yaml:"tools,omitempty" json:"tools,omitempty"`
	// Exec is the operator-supplied entrypoint override (e.g. a
	// custom runtime invocation). Lives in daemon.db; never on
	// disk in agent.yaml.
	Exec    []string `yaml:"-" json:"-"`
	Addr    string   `yaml:"-" json:"-"`
	APIBase string   `yaml:"-" json:"-"`
	APIKey  string   `yaml:"-" json:"-"`
}

// OCIRef pairs an image reference with its content-addressed digest.
// Same shape on image, runtime, and per-tool — read the same way
// across all three.
type OCIRef struct {
	Ref    string `yaml:"ref" json:"ref"`
	Digest string `yaml:"digest,omitempty" json:"digest,omitempty"`
}

// ResolvedTool describes a tool binary with its resolved filesystem
// path plus, when known, its source ref + OCI digest and the
// in-workspace path to its USAGE.md.
type ResolvedTool struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Binary      string `yaml:"binary" json:"binary"`
	Ref         string `yaml:"ref,omitempty" json:"ref,omitempty"`
	Digest      string `yaml:"digest,omitempty" json:"digest,omitempty"`
	Usage       string `yaml:"usage,omitempty" json:"usage,omitempty"`
}

// WriteTo serializes the runtime to the given filesystem as YAML.
func (rt *Runtime) WriteTo(fs billy.Filesystem) error {
	data, err := yaml.Marshal(rt)
	if err != nil {
		return fmt.Errorf("marshaling runtime: %w", err)
	}

	return util.WriteFile(fs, runtimeFile, data, 0o644)
}

// LoadRuntime reads an Runtime from the given filesystem.
func LoadRuntime(fs billy.Filesystem) (*Runtime, error) {
	data, err := util.ReadFile(fs, runtimeFile)
	if err != nil {
		return nil, fmt.Errorf("reading runtime: %w", err)
	}

	var rt Runtime
	if e := yaml.Unmarshal(data, &rt); e != nil {
		return nil, fmt.Errorf("parsing runtime: %w", e)
	}

	return &rt, nil
}
