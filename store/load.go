// Package store reads OCI manifests and Agentfiles out of oras targets.
// Two entry points:
//
//   - Load returns the raw manifest and parsed Agentfile.
//   - LoadHydrated additionally pulls every layer and populates
//     Agentfile.Agent.Contexts[*].Content and Agent.Adds[*].Content, so the
//     returned value is self-contained (used when a parent agentfile is
//     consumed by a child build that has no access to the parent's source
//     filesystem).
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"

	"github.com/openotters/agentfile/spec"
)

// Load resolves ref in s and returns the raw manifest and parsed
// Agentfile. The Agentfile JSON lives in a dedicated layer
// (mediatype spec.AgentConfigLayerMediatype) — manifest.Config is
// a standard image config so docker's cli.ImageInspect can surface
// the artifact-type Label on the daemon-side fast path. Layer
// contents (Context.Content, Add.Content) are not hydrated; use
// LoadHydrated when callers need those fields populated.
func Load(ctx context.Context, s oras.ReadOnlyTarget, ref spec.Reference) (*v1.Manifest, *spec.Agentfile, error) {
	desc, err := s.Resolve(ctx, ref.String())
	if err != nil {
		return nil, nil, fmt.Errorf("resolving manifest: %w", err)
	}

	manifestData, err := fetchBytes(ctx, s, desc)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching manifest: %w", err)
	}

	var manifest v1.Manifest
	if err = json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, nil, fmt.Errorf("parsing manifest: %w", err)
	}

	var agentfileLayer *v1.Descriptor

	for i, layer := range manifest.Layers {
		if layer.MediaType == spec.AgentConfigLayerMediatype {
			agentfileLayer = &manifest.Layers[i]

			break
		}
	}

	if agentfileLayer == nil {
		return nil, nil, fmt.Errorf("agentfile layer (%s) not found in manifest", spec.AgentConfigLayerMediatype)
	}

	agentfileData, err := fetchBytes(ctx, s, *agentfileLayer)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching agentfile layer: %w", err)
	}

	var af spec.Agentfile
	if err = json.Unmarshal(agentfileData, &af); err != nil {
		return nil, nil, fmt.Errorf("parsing agentfile: %w", err)
	}

	return &manifest, &af, nil
}

// LoadHydrated loads ref and additionally pulls every layer, populating
// Agentfile.Agent.Contexts[*].Content and Agent.Adds[*].Content so the
// returned Agentfile is self-contained. Needed for FROM inheritance: a
// child build consumes a parent Agentfile without access to the parent's
// source filesystem.
func LoadHydrated(ctx context.Context, s oras.ReadOnlyTarget, ref spec.Reference) (*spec.Agentfile, error) {
	manifest, af, err := Load(ctx, s, ref)
	if err != nil {
		return nil, err
	}

	if af.Agent == nil {
		return af, nil
	}

	contextData := make(map[string][]byte)
	addData := make(map[string][]byte)

	for _, layer := range manifest.Layers {
		title := layer.Annotations[v1.AnnotationTitle]
		if title == "" {
			continue
		}

		data, fetchErr := fetchBytes(ctx, s, layer)
		if fetchErr != nil {
			continue
		}

		switch layer.MediaType {
		case spec.ContextLayerMediaType:
			name := strings.TrimSuffix(title, ".md")
			contextData[name] = data
		case spec.OctetStream:
			addData[title] = data
		}
	}

	for _, c := range af.Agent.Contexts {
		if c.Content == "" {
			if data, ok := contextData[c.Name]; ok {
				c.Content = string(data)
				c.File = ""
			}
		}
	}

	for _, a := range af.Agent.Adds {
		if data, ok := addData[a.Dst]; ok {
			a.Content = data
		}
	}

	return af, nil
}

func fetchBytes(ctx context.Context, s oras.ReadOnlyTarget, desc v1.Descriptor) ([]byte, error) {
	rc, err := s.Fetch(ctx, desc)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	return io.ReadAll(rc)
}
