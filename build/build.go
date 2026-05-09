// Package build packs an Agentfile and its referenced contexts and bins
// into an OCI artifact that can be pushed to a registry or inspected as a
// standalone manifest. Inherited parents are resolved transparently.
package build

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/osfs"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"

	"github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/resolve"
	"github.com/openotters/agentfile/spec"
)

// FromFile parses an Agentfile, resolves FROM inheritance, and builds the
// OCI artifact into target. Returns the image reference with digest.
func FromFile(ctx context.Context, agentfilePath string, target oras.Target) (*spec.ReferenceWithDigest, error) {
	af, err := spec.ParseFile(agentfilePath)
	if err != nil {
		return nil, fmt.Errorf("parsing: %w", err)
	}

	resolvedaf, err := resolve.Resolve(ctx, af, oci.AgentFetcher())
	if err != nil {
		return nil, fmt.Errorf("resolving: %w", err)
	}

	srcDir, _ := filepath.Abs(filepath.Dir(agentfilePath))

	ref, err := Build(ctx, resolvedaf, osfs.New(srcDir), target)
	if err != nil {
		return nil, fmt.Errorf("building: %w", err)
	}

	return ref, nil
}

// Build creates an OCI artifact from a parsed Agentfile and pushes it into dst.
// Context and ADD files are read from src. The manifest is tagged with the agent
// name in dst. Returns the image reference with digest.
func Build(
	ctx context.Context,
	af *spec.Agentfile,
	src billy.Filesystem,
	dst oras.Target,
) (*spec.ReferenceWithDigest, error) {
	agent := af.Agent

	var layers []v1.Descriptor

	for _, c := range agent.Contexts {
		ct, err := resolveContextContent(c, src)
		if err != nil {
			return nil, fmt.Errorf("context %s: %w", c.Name, err)
		}

		annotations := map[string]string{v1.AnnotationTitle: c.Name + ".md"}
		desc, err := pushBlob(ctx, dst, spec.ContextLayerMediaType, ct, annotations)
		if err != nil {
			return nil, fmt.Errorf("pushing context %s: %w", c.Name, err)
		}

		layers = append(layers, desc)
	}

	for _, a := range agent.Adds {
		var data []byte

		if len(a.Content) > 0 {
			data = a.Content
		} else {
			var readErr error
			data, readErr = readFile(src, a.Src)
			if readErr != nil {
				return nil, fmt.Errorf("reading %s: %w", a.Src, readErr)
			}
		}

		annotations := map[string]string{v1.AnnotationTitle: a.Dst}
		desc, err := pushBlob(ctx, dst, spec.OctetStream, data, annotations)
		if err != nil {
			return nil, fmt.Errorf("pushing file %s: %w", a.Src, err)
		}

		layers = append(layers, desc)
	}

	return packManifest(ctx, dst, af, layers)
}

func packManifest(
	ctx context.Context,
	dst oras.Target,
	af *spec.Agentfile,
	layers []v1.Descriptor,
) (*spec.ReferenceWithDigest, error) {
	// The agentfile JSON travels as a dedicated layer (mediatype
	// AgentConfigLayerMediatype) so manifest.Config can be a
	// standard image config with Labels — the only path docker's
	// cli.ImageInspect surfaces, which is what the listing fast
	// path reads. afstore.Load picks up the agentfile from
	// whichever layer carries that mediatype.
	agentfileData, err := json.MarshalIndent(af, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling agentfile: %w", err)
	}

	agentfileDesc, err := pushBlob(ctx, dst, spec.AgentConfigLayerMediatype, agentfileData, nil)
	if err != nil {
		return nil, fmt.Errorf("pushing agentfile layer: %w", err)
	}

	if agentfileDesc.Annotations == nil {
		agentfileDesc.Annotations = make(map[string]string)
	}

	agentfileDesc.Annotations[v1.AnnotationTitle] = "agentfile.json"

	configLabels := map[string]string{
		spec.LabelArtifactType: spec.AgentArtifactType,
	}
	if desc := af.Agent.Labels[v1.AnnotationDescription]; desc != "" {
		configLabels[v1.AnnotationDescription] = desc
	}
	if src := af.Agent.Labels[v1.AnnotationSource]; src != "" {
		configLabels[v1.AnnotationSource] = src
	}

	imgConfig := v1.Image{
		Platform: v1.Platform{Architecture: "unknown", OS: "unknown"},
		Config: v1.ImageConfig{
			Labels: configLabels,
		},
		RootFS: v1.RootFS{Type: "layers"},
	}

	imgConfigData, err := json.Marshal(imgConfig)
	if err != nil {
		return nil, fmt.Errorf("marshaling image config: %w", err)
	}

	configDesc, err := pushBlob(ctx, dst, v1.MediaTypeImageConfig, imgConfigData, nil)
	if err != nil {
		return nil, fmt.Errorf("pushing image config: %w", err)
	}

	annotations := make(map[string]string)
	for k, v := range af.Agent.Labels {
		annotations[k] = v
	}

	if af.Agent.Name != "" {
		annotations[v1.AnnotationTitle] = af.Agent.Name
	}

	allLayers := append([]v1.Descriptor{agentfileDesc}, layers...)

	manifest := v1.Manifest{
		Versioned:   specs.Versioned{SchemaVersion: 2},
		MediaType:   v1.MediaTypeImageManifest,
		Config:      configDesc,
		Layers:      allLayers,
		Annotations: annotations,
	}
	manifest.ArtifactType = spec.AgentArtifactType

	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("marshaling manifest: %w", err)
	}

	manifestDesc := v1.Descriptor{
		MediaType: v1.MediaTypeImageManifest,
		Digest:    digestOf(manifestData),
		Size:      int64(len(manifestData)),
	}

	if err = dst.Push(ctx, manifestDesc, bytes.NewReader(manifestData)); err != nil {
		return nil, fmt.Errorf("pushing manifest: %w", err)
	}

	// Tag with the digest so it's always resolvable by content address.
	if err = dst.Tag(ctx, manifestDesc, manifestDesc.Digest.String()); err != nil {
		return nil, fmt.Errorf("tagging manifest with digest: %w", err)
	}

	ref := spec.Reference{Name: af.Agent.Name, Tag: spec.DefaultTag}

	// Tag with "name:latest" so it's resolvable by name.
	if af.Agent.Name != "" {
		if err = dst.Tag(ctx, manifestDesc, ref.String()); err != nil {
			return nil, fmt.Errorf("tagging manifest with name: %w", err)
		}
	}

	return &spec.ReferenceWithDigest{
		Reference: ref,
		Digest:    manifestDesc.Digest,
	}, nil
}

func pushBlob(
	ctx context.Context, dst content.Pusher, mediaType string, data []byte, annotations map[string]string,
) (v1.Descriptor, error) {
	desc := v1.Descriptor{
		MediaType:   mediaType,
		Digest:      digestOf(data),
		Size:        int64(len(data)),
		Annotations: annotations,
	}

	if err := dst.Push(ctx, desc, bytes.NewReader(data)); err != nil {
		return v1.Descriptor{}, fmt.Errorf("pushing blob: %w", err)
	}

	return desc, nil
}

func digestOf(data []byte) digest.Digest {
	h := sha256.Sum256(data)
	return digest.NewDigestFromBytes(digest.SHA256, h[:])
}

func resolveContextContent(c *spec.Context, src billy.Filesystem) ([]byte, error) {
	if c.Content != "" {
		return []byte(c.Content), nil
	}

	if c.File != "" && src != nil {
		return readFile(src, c.File)
	}

	return nil, nil
}

func readFile(fs billy.Filesystem, path string) ([]byte, error) {
	f, err := fs.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return io.ReadAll(f)
}
