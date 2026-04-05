package store

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/openotters/agentfile/spec"
	"oras.land/oras-go/v2/content/memory"
)

// Load resolves a manifest by ref and deserializes the Agentfile from the config blob.
func Load(s *memory.Store, ref string) (*spec.Agentfile, error) {
	_, af, err := loadManifestAndConfig(s, ref)
	return af, err
}

// LoadWithLayers loads the Agentfile and hydrates Add.Content and Context.Content
// from the OCI layers. This is needed for FROM inheritance: the parent's contexts
// and data files are embedded in the returned Agentfile so that a child build
// doesn't need access to the parent's source filesystem.
func LoadWithLayers(s *memory.Store, ref string) (*spec.Agentfile, error) {
	manifest, af, err := loadManifestAndConfig(s, ref)
	if err != nil {
		return nil, err
	}

	if af.Agent == nil {
		return af, nil
	}

	ctx := context.Background()

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

func loadManifestAndConfig(s *memory.Store, ref string) (*v1.Manifest, *spec.Agentfile, error) {
	ctx := context.Background()

	desc, err := s.Resolve(ctx, ref)
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

	configData, err := fetchBytes(ctx, s, manifest.Config)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching config: %w", err)
	}

	var af spec.Agentfile
	if err = json.Unmarshal(configData, &af); err != nil {
		return nil, nil, fmt.Errorf("parsing agentfile: %w", err)
	}

	return &manifest, &af, nil
}

func fetchBytes(ctx context.Context, s *memory.Store, desc v1.Descriptor) ([]byte, error) {
	rc, err := s.Fetch(ctx, desc)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	return io.ReadAll(rc)
}
