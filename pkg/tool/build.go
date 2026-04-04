package tool

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
	"github.com/openotters/agentfile/pkg/utils"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
)

// BuildOptions configures a tool image build.
type BuildOptions struct {
	Name        string
	BinPath     string
	Description string
	Usage       string
}

// Build creates a single-platform OCI tool image and pushes it into dst.
// The binary is read from src at opts.BinPath. If opts.Usage is non-empty,
// a USAGE.md layer is added. Manifest annotations are set per the spec.
// The manifest is tagged as "latest" in dst.
func Build(ctx context.Context, opts BuildOptions, src billy.Filesystem, dst oras.Target) (*digest.Digest, error) {
	return buildPlatform(ctx, opts, src, dst, "latest")
}

// PlatformBuild describes a single platform variant for a multi-arch build.
type PlatformBuild struct {
	OS   string
	Arch string
	Src  billy.Filesystem
}

// BuildIndex builds per-platform tool images and assembles an OCI image index.
// Each platform's binary is read from its Src filesystem at opts.BinPath.
// The index is tagged as "latest" in dst.
func BuildIndex(
	ctx context.Context, opts BuildOptions, platforms []PlatformBuild, dst oras.Target,
) (*digest.Digest, error) {
	var manifests []v1.Descriptor

	for _, p := range platforms {
		platformTag := fmt.Sprintf("latest-%s-%s", p.OS, p.Arch)

		_, err := buildPlatform(ctx, opts, p.Src, dst, platformTag)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", p.OS, p.Arch, err)
		}

		desc, err := dst.Resolve(ctx, platformTag)
		if err != nil {
			return nil, fmt.Errorf("resolving %s: %w", platformTag, err)
		}

		desc.Platform = &v1.Platform{OS: p.OS, Architecture: p.Arch}
		manifests = append(manifests, desc)
	}

	index := v1.Index{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: v1.MediaTypeImageIndex,
		Manifests: manifests,
	}

	indexData, err := json.Marshal(index)
	if err != nil {
		return nil, fmt.Errorf("marshaling index: %w", err)
	}

	indexDesc := v1.Descriptor{
		MediaType: v1.MediaTypeImageIndex,
		Digest:    digestOf(indexData),
		Size:      int64(len(indexData)),
	}

	if err = dst.Push(ctx, indexDesc, bytes.NewReader(indexData)); err != nil {
		return nil, fmt.Errorf("pushing index: %w", err)
	}

	if err = dst.Tag(ctx, indexDesc, "latest"); err != nil {
		return nil, fmt.Errorf("tagging index: %w", err)
	}

	d := indexDesc.Digest
	return &d, nil
}

// buildPlatform is Build but tags with a custom tag instead of "latest".
func buildPlatform(
	ctx context.Context, opts BuildOptions, src billy.Filesystem, dst oras.Target, tag string,
) (*digest.Digest, error) {
	binData, err := readFile(src, opts.BinPath)
	if err != nil {
		return nil, fmt.Errorf("reading binary %s: %w", opts.BinPath, err)
	}

	var layers []v1.Descriptor

	binDesc, err := pushBlob(ctx, dst, utils.OctetStream, binData, map[string]string{
		v1.AnnotationTitle: opts.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("pushing binary: %w", err)
	}

	layers = append(layers, binDesc)

	annotations := map[string]string{
		AnnotationToolBin: opts.Name,
	}

	if opts.Description != "" {
		annotations[AnnotationToolDescription] = opts.Description
	}

	if opts.Usage != "" {
		usageName := "USAGE.md"
		annotations[AnnotationToolUsage] = usageName

		usageDesc, usageErr := pushBlob(ctx, dst, utils.Markdown, []byte(opts.Usage), map[string]string{
			v1.AnnotationTitle: usageName,
		})
		if usageErr != nil {
			return nil, fmt.Errorf("pushing usage: %w", usageErr)
		}

		layers = append(layers, usageDesc)
	}

	configData := []byte("{}")

	configDesc, err := pushBlob(ctx, dst, v1.MediaTypeImageConfig, configData, nil)
	if err != nil {
		return nil, fmt.Errorf("pushing config: %w", err)
	}

	manifest := v1.Manifest{
		Versioned:   specs.Versioned{SchemaVersion: 2},
		MediaType:   v1.MediaTypeImageManifest,
		Config:      configDesc,
		Layers:      layers,
		Annotations: annotations,
	}

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

	if err = dst.Tag(ctx, manifestDesc, tag); err != nil {
		return nil, fmt.Errorf("tagging manifest: %w", err)
	}

	d := manifestDesc.Digest
	return &d, nil
}

func readFile(fs billy.Filesystem, path string) ([]byte, error) {
	f, err := fs.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return io.ReadAll(f)
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
