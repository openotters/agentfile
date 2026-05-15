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
	Runtime   *RuntimeRef       `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Configs   map[string]string `yaml:"configs,omitempty" json:"configs,omitempty"`
	// Capabilities enumerates the daemon-callback tools the runtime
	// is expected to expose to the model (job_submit, job_wait, …).
	// Populated by the daemon at materialise time based on whether
	// it's wiring an OTTERSD_URL + agent token at spawn. Purely
	// declarative today — the runtime still gates registration on
	// the spawn env. A future Agentfile directive will make this
	// list operator-controllable.
	Capabilities []string       `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	Envs         EnvKeys        `yaml:"envs,omitempty" json:"envs,omitempty"`
	Mounts       []*spec.Mount  `yaml:"mounts,omitempty" json:"mounts,omitempty"`
	Context      []string       `yaml:"context,omitempty" json:"context,omitempty"`
	Tools        []ResolvedTool `yaml:"tools,omitempty" json:"tools,omitempty"`
	// Exec is the operator-supplied entrypoint override (e.g. a
	// custom runtime invocation). Lives in daemon.db; never on
	// disk in agent.yaml.
	Exec    []string `yaml:"-" json:"-"`
	Addr    string   `yaml:"-" json:"-"`
	APIBase string   `yaml:"-" json:"-"`
	APIKey  string   `yaml:"-" json:"-"`
}

// OCIRef pairs an image reference with its content-addressed digest.
// Used for the agent image and per-tool refs.
type OCIRef struct {
	Ref    string `yaml:"ref" json:"ref"`
	Digest string `yaml:"digest,omitempty" json:"digest,omitempty"`
}

// RuntimeRef is OCIRef plus the in-container path of the runtime
// binary. The path is what the executor uses as the container's
// entrypoint (e.g. `/opt/runtime/runtime` on docker) — surfacing it
// here lets `otters inspect` answer "where does the agent's
// runtime live?" without inferring from the executor backend.
type RuntimeRef struct {
	Ref    string `yaml:"ref" json:"ref"`
	Digest string `yaml:"digest,omitempty" json:"digest,omitempty"`
	Binary string `yaml:"binary,omitempty" json:"binary,omitempty"`
}

// EnvKeys is the disk shape for the spec's env declarations. On
// disk it serialises as a list of bare key strings — values never
// touch agent.yaml (the daemon hydrates them in-memory from the
// Agentfile defaults + operator overrides in daemon.db at spawn
// time). In-memory it remains []*spec.Env so the executor's
// AppendUserEnv keeps working unchanged.
type EnvKeys []*spec.Env

// MarshalYAML writes the env list as `[]string` of keys — no
// objects, no descriptions, no values. Descriptions live in the
// Agentfile spec (and surface through `otters inspect`'s spec
// view); the runtime-side agent.yaml only declares "this agent
// expects these keys.".
func (e EnvKeys) MarshalYAML() (any, error) {
	keys := make([]string, 0, len(e))
	for _, env := range e {
		if env == nil || env.Key == "" {
			continue
		}
		keys = append(keys, env.Key)
	}
	return keys, nil
}

// UnmarshalYAML accepts either the bare-string list shape (new) or
// the legacy `[]{key, description}` list-of-objects shape so old
// agent.yaml files still load cleanly until the next materialise
// rewrites them.
func (e *EnvKeys) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.SequenceNode {
		return fmt.Errorf("envs: expected a sequence, got %v", node.Kind)
	}
	out := make([]*spec.Env, 0, len(node.Content))
	for _, item := range node.Content {
		switch item.Kind {
		case yaml.ScalarNode:
			out = append(out, &spec.Env{Key: item.Value})
		case yaml.MappingNode:
			var legacy struct {
				Key         string `yaml:"key"`
				Description string `yaml:"description"`
			}
			if err := item.Decode(&legacy); err != nil {
				return fmt.Errorf("envs: %w", err)
			}
			out = append(out, &spec.Env{Key: legacy.Key, Description: legacy.Description})
		case yaml.DocumentNode, yaml.SequenceNode, yaml.AliasNode:
			return fmt.Errorf("envs: unsupported node kind %v", item.Kind)
		}
	}
	*e = out
	return nil
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
