package build

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"

	"github.com/go-git/go-billy/v6"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/openotters/agentfile/pkg/agentfile"
	"github.com/openotters/agentfile/pkg/utils"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
)

// Build creates an OCI artifact from a parsed Agentfile and pushes it into dst.
// ContextLayerMediaType content and ADD files are read from src. The manifest is tagged as "latest"
// in dst. Returns the manifest digest.
func Build(
	ctx context.Context, af *agentfile.Agentfile, src billy.Filesystem, dst oras.Target,
) (*digest.Digest, error) {
	agent := af.Agent

	var layers []v1.Descriptor

	for _, c := range agent.Contexts {
		ct, err := resolveContextContent(c, src)
		if err != nil {
			return nil, fmt.Errorf("context %s: %w", c.Name, err)
		}

		annotations := map[string]string{v1.AnnotationTitle: c.Name + ".md"}
		desc, err := pushBlob(ctx, dst, ContextLayerMediaType, ct, annotations)
		if err != nil {
			return nil, fmt.Errorf("pushing context %s: %w", c.Name, err)
		}

		layers = append(layers, desc)
	}

	for _, a := range agent.Adds {
		data, err := readFile(src, a.Src)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", a.Src, err)
		}

		annotations := map[string]string{v1.AnnotationTitle: a.Dst}
		desc, err := pushBlob(ctx, dst, utils.OctetStream, data, annotations)
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
	af *agentfile.Agentfile,
	layers []v1.Descriptor,
) (*digest.Digest, error) {
	configData, err := json.MarshalIndent(af, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling config: %w", err)
	}

	configDesc, err := pushBlob(ctx, dst, AgentConfigLayerMediatype, configData, nil)
	if err != nil {
		return nil, fmt.Errorf("pushing config: %w", err)
	}

	annotations := make(map[string]string)
	for k, v := range af.Agent.Labels {
		annotations[k] = v
	}

	if af.Agent.Name != "" {
		annotations[v1.AnnotationTitle] = af.Agent.Name
	}

	manifest := v1.Manifest{
		Versioned:   specs.Versioned{SchemaVersion: 2},
		MediaType:   v1.MediaTypeImageManifest,
		Config:      configDesc,
		Layers:      layers,
		Annotations: annotations,
	}
	manifest.Config.MediaType = AgentConfigLayerMediatype
	manifest.ArtifactType = AgentArtifactType

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

	if err = dst.Tag(ctx, manifestDesc, "latest"); err != nil {
		return nil, fmt.Errorf("tagging manifest: %w", err)
	}

	d := manifestDesc.Digest
	return &d, nil
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

func resolveContextContent(c *agentfile.Context, src billy.Filesystem) ([]byte, error) {
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
