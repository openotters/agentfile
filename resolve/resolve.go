// Package resolve resolves FROM inheritance by pulling parent agent artifacts
// and merging them with child instructions.
//
// The pipeline is: parse → resolve → build → execute.
//
// FROM scratch is a no-op. FROM <ref> pulls the parent, recursively resolves
// its own FROM, then merges according to the inheritance rules:
//
//   - RUNTIME: child overrides parent, clears configs
//   - MODEL, NAME: child overrides parent
//   - CONTEXT: same-name overrides parent, new names appended
//   - CONFIG: appended (cleared if child sets RUNTIME)
//   - BIN: appended
//   - ADD: appended
//   - LABEL: merged (child wins on key conflicts)
package resolve

import (
	"context"
	"fmt"

	"github.com/openotters/agentfile/spec"
)

const maxDepth = 10

// Fetcher pulls a parent agent artifact by OCI reference and returns the parsed Agentfile.
type Fetcher func(ctx context.Context, ref string) (*spec.Agentfile, error)

// Resolve resolves FROM inheritance. If af.Agent.From is "scratch", the Agentfile is
// returned as-is. Otherwise, the parent is fetched, recursively resolved, and merged.
func Resolve(ctx context.Context, af *spec.Agentfile, fetch Fetcher) (*spec.Agentfile, error) {
	return resolveDepth(ctx, af, fetch, 0)
}

func resolveDepth(ctx context.Context, af *spec.Agentfile, fetch Fetcher, depth int) (*spec.Agentfile, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("FROM inheritance depth exceeds %d (circular reference?)", maxDepth)
	}

	if af.Agent.From == "" || af.Agent.From == "scratch" {
		return af, nil
	}

	parent, err := fetch(ctx, af.Agent.From)
	if err != nil {
		return nil, fmt.Errorf("pulling parent %s: %w", af.Agent.From, err)
	}

	parent, err = resolveDepth(ctx, parent, fetch, depth+1)
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

	return merged
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
