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

// Runtime is the persistable result of creating an agent.
// It contains the original spec (source of truth) and the resolved
// configuration.
type Runtime struct {
	ID             uuid.UUID       `yaml:"id" json:"id"`
	Source         *spec.Agentfile `yaml:"source" json:"source"`
	ResolvedConfig `yaml:",inline" json:",inline"`
}

// ResolvedConfig holds the merged configuration after applying spec
// overrides. APIBase and APIKey are intentionally non-serialised
// (`yaml:"-"` / `json:"-"`): they are credentials resolved from
// providers.yaml on every (re)start and travel to the runtime
// subprocess via env (<PROVIDER>_API_KEY / <PROVIDER>_API_BASE),
// not via disk or argv. Older agent.yaml files that contain these
// fields are tolerated on load — yaml/json simply skip them.
type ResolvedConfig struct {
	Name       string         `yaml:"name" json:"name"`
	Model      string         `yaml:"model" json:"model"`
	Addr       string         `yaml:"addr,omitempty" json:"addr,omitempty"`
	APIBase    string         `yaml:"-" json:"-"`
	APIKey     string         `yaml:"-" json:"-"`
	Exec       []string       `yaml:"exec,omitempty" json:"exec,omitempty"`
	Provenance *Provenance    `yaml:"provenance,omitempty" json:"provenance,omitempty"`
	Tools      []ResolvedTool `yaml:"tools,omitempty" json:"tools,omitempty"`
}

// Provenance records the OCI digests of the artifacts that materialise
// an agent: the agent image itself, the runtime image it executes
// against, and the BIN tool images per ResolvedTool. Optional —
// populated only when the workspace is given a digest resolver
// (today: the openotters daemon's local registry resolver). Lets a
// host operator answer "exactly which bytes is this agent running?"
// without re-querying any registry.
type Provenance struct {
	ImageDigest   string `yaml:"image_digest,omitempty" json:"image_digest,omitempty"`
	RuntimeRef    string `yaml:"runtime_ref,omitempty" json:"runtime_ref,omitempty"`
	RuntimeDigest string `yaml:"runtime_digest,omitempty" json:"runtime_digest,omitempty"`
}

// ResolvedTool describes a tool binary with its resolved filesystem
// path plus, when known, its source ref and OCI digest.
type ResolvedTool struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Binary      string   `yaml:"binary" json:"binary"`
	Ref         string   `yaml:"ref,omitempty" json:"ref,omitempty"`
	Digest      string   `yaml:"digest,omitempty" json:"digest,omitempty"`
	Docs        ToolDocs `yaml:"docs,omitempty" json:"docs,omitempty"`
}

// ToolDocs are the documentation artefacts materialised alongside the
// BIN binary. Each field is a filesystem path the runtime can read
// when assembling the model-facing tool description: relative paths
// resolve against the agent's chroot root, absolute paths are used
// verbatim (the docker executor uses absolute container-rooted
// paths). Empty means "this BIN has no doc of that kind."
//
// The shape is intentionally a small struct rather than a flat
// scalar so additional artefacts (examples, schema, FAQ) can land
// here without breaking the agent.yaml schema. Producers populate
// each field from the corresponding `vnd.openotters.bin.*` manifest
// annotation at materialisation time.
type ToolDocs struct {
	// Usage is the path to a USAGE.md-style long-form description.
	// Sourced from the BIN image's `vnd.openotters.bin.usage`
	// annotation. Optional — empty for BINs that ship no doc.
	Usage string `yaml:"usage,omitempty" json:"usage,omitempty"`
}

// WriteTo serializes the runtime to the given filesystem as YAML.
//
// Source's nested types (spec.Agentfile, spec.Agent, …) carry json+yaml
// tags via the spec package; the linter's musttag check only inspects
// the top-level concrete type and misses the transitive coverage.
//
//nolint:musttag // tags live on the embedded spec.* types; see comment
func (rt *Runtime) WriteTo(fs billy.Filesystem) error {
	data, err := yaml.Marshal(rt)
	if err != nil {
		return fmt.Errorf("marshaling runtime: %w", err)
	}

	return util.WriteFile(fs, runtimeFile, data, 0o644)
}

// LoadRuntime reads an Runtime from the given filesystem.
//
//nolint:musttag // tags live on the embedded spec.* types; see WriteTo
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
