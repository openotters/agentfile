// Package resolve resolves FROM inheritance by pulling parent agent artifacts
// and merging them with child instructions.
//
// The pipeline is: parse → resolve → build → execute.
//
// FROM scratch is a no-op. FROM <ref> pulls the parent, recursively resolves
// its own FROM, then merges according to the spec's inheritance table
// (AGENTFILE-v1.0.0.md, Merge Semantics):
//
//   - RUNTIME: replace; if the child sets it, all parent CONFIGs are dropped
//   - MODEL, NAME, EXEC: replace
//   - CONTEXT: keyed-merge by name
//   - CONFIG: append (dropped entirely if child sets RUNTIME)
//   - BIN, ADD, ENV: append
//   - LABEL, ARG: keyed-merge (child wins)
//   - CAPABILITY: set-union
//
// The parent chain must be acyclic; a repeated ref is a resolve error.
package resolve

import (
	"context"
	"fmt"
	"strings"

	"github.com/openotters/agentfile/spec"
)

const maxDepth = 10

// Fetcher pulls a parent agent artifact by OCI reference and returns the parsed Agentfile.
type Fetcher func(ctx context.Context, ref spec.Reference) (*spec.Agentfile, error)

// Resolve resolves FROM inheritance. If af.Agent.From is "scratch", the Agentfile is
// returned as-is. Otherwise, the parent is fetched, recursively resolved, and merged.
// A cycle in the parent chain is an error.
func Resolve(ctx context.Context, af *spec.Agentfile, fetch Fetcher) (*spec.Agentfile, error) {
	return resolveDepth(ctx, af, fetch, 0, nil)
}

func resolveDepth(
	ctx context.Context, af *spec.Agentfile, fetch Fetcher, depth int, chain []string,
) (*spec.Agentfile, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("FROM inheritance depth exceeds %d", maxDepth)
	}

	if af.Agent.From == "" || af.Agent.From == "scratch" {
		return af, nil
	}

	// Detect cycles by normalized ref before fetching: a ref already on
	// the chain means the ancestry loops back on itself.
	ref := spec.ParseReference(af.Agent.From)
	normalized := ref.String()

	for _, seen := range chain {
		if seen == normalized {
			return nil, fmt.Errorf(
				"inheritance cycle: %s", strings.Join(append(chain, normalized), " -> "),
			)
		}
	}

	parent, err := fetch(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("pulling parent %s: %w", af.Agent.From, err)
	}

	parent, err = resolveDepth(ctx, parent, fetch, depth+1, append(chain, normalized))
	if err != nil {
		return nil, err
	}

	return merge(parent, af), nil
}

func merge(parent, child *spec.Agentfile) *spec.Agentfile {
	result := &spec.Agentfile{
		Syntax: child.Syntax,
		Agent:  mergeAgent(parent.Agent, child.Agent),
	}

	if result.Syntax == "" {
		result.Syntax = parent.Syntax
	}

	return result
}

func mergeAgent(parent, child *spec.Agent) *spec.Agent {
	merged := &spec.Agent{
		From:   child.From,
		Labels: make(map[string]string),
		Args:   make(map[string]string),
	}

	// Scalars: child overrides parent
	merged.Runtime = parent.Runtime
	merged.Model = parent.Model
	merged.Name = parent.Name

	if child.Runtime != "" {
		merged.Runtime = child.Runtime
	}

	if child.Model != "" {
		merged.Model = child.Model
	}

	if child.Name != "" {
		merged.Name = child.Name
	}

	// Exec: replace — the child's invocation wins entirely when set.
	merged.Exec = parent.Exec
	if len(child.Exec) > 0 {
		merged.Exec = child.Exec
	}

	// Contexts: same-name overrides, new names appended
	merged.Contexts = mergeContexts(parent.Contexts, child.Contexts)

	// Configs: if child sets RUNTIME, parent configs are dropped
	if child.Runtime != "" {
		merged.Configs = cloneConfigs(child.Configs)
	} else {
		merged.Configs = append(cloneConfigs(parent.Configs), child.Configs...)
	}

	// Bins: appended
	merged.Bins = append(cloneBins(parent.Bins), child.Bins...)

	// Adds: appended
	merged.Adds = append(cloneAdds(parent.Adds), child.Adds...)

	// Envs: appended — flattened by keyed-merge (last wins) at spawn,
	// so the child's same-key declaration overrides the parent's there.
	merged.Envs = append(cloneEnvs(parent.Envs), child.Envs...)

	// Labels: merged, child wins
	for k, v := range parent.Labels {
		merged.Labels[k] = v
	}

	for k, v := range child.Labels {
		merged.Labels[k] = v
	}

	// Args: merged, child wins
	for k, v := range parent.Args {
		merged.Args[k] = v
	}

	for k, v := range child.Args {
		merged.Args[k] = v
	}

	// Capabilities: union, dedup. A child Agentfile adds to its
	// parent's surface; there's no way to drop a parent's cap
	// from within an Agentfile today (operator override via
	// --cap can replace the whole set if needed).
	merged.Capabilities = mergeStringSet(parent.Capabilities, child.Capabilities)

	return merged
}

func mergeStringSet(parent, child []string) []string {
	if len(parent) == 0 && len(child) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(parent)+len(child))
	out := make([]string, 0, len(parent)+len(child))
	for _, s := range parent {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, s := range child {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func mergeContexts(parent, child []*spec.Context) []*spec.Context {
	byName := make(map[string]int)
	var result []*spec.Context

	for _, c := range parent {
		byName[c.Name] = len(result)
		result = append(result, c)
	}

	for _, c := range child {
		if idx, ok := byName[c.Name]; ok {
			result[idx] = c
		} else {
			byName[c.Name] = len(result)
			result = append(result, c)
		}
	}

	return result
}

func cloneConfigs(configs []*spec.Config) []*spec.Config {
	if configs == nil {
		return nil
	}

	out := make([]*spec.Config, len(configs))
	copy(out, configs)

	return out
}

func cloneBins(bins []*spec.Bin) []*spec.Bin {
	if bins == nil {
		return nil
	}

	out := make([]*spec.Bin, len(bins))
	copy(out, bins)

	return out
}

func cloneAdds(adds []*spec.Add) []*spec.Add {
	if adds == nil {
		return nil
	}

	out := make([]*spec.Add, len(adds))
	copy(out, adds)

	return out
}

func cloneEnvs(envs []*spec.Env) []*spec.Env {
	if envs == nil {
		return nil
	}

	out := make([]*spec.Env, len(envs))
	copy(out, envs)

	return out
}
