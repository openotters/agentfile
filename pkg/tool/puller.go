package tool

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/openotters/agentfile/pkg/utils"
	"oras.land/oras-go/v2/content"
)

// NoopPuller writes a placeholder shell script. Useful for testing and examples.
func NoopPuller(_ context.Context, _ string, w io.Writer) error {
	_, err := w.Write([]byte("#!/bin/sh\n"))
	return err
}

// RemotePuller returns a puller function that fetches tool binaries from remote OCI registries.
// It resolves image indexes (multi-arch) to the current platform automatically.
func RemotePuller(opts ...utils.RemoteRepositoryOption) func(ctx context.Context, ref string, w io.Writer) error {
	return func(ctx context.Context, ref string, w io.Writer) error {
		repo, err := utils.NewRemoteRepository(ref, opts...)
		if err != nil {
			return err
		}

		tag := repo.Reference.Reference
		if tag == "" {
			tag = "latest"
		}

		desc, err := repo.Resolve(ctx, tag)
		if err != nil {
			return fmt.Errorf("resolving %s: %w", ref, err)
		}

		manifest, err := utils.ResolveManifest(ctx, repo, desc)
		if err != nil {
			return err
		}

		return writeBin(ctx, repo, *manifest, w)
	}
}

func writeBin(ctx context.Context, fetcher content.Fetcher, manifest v1.Manifest, w io.Writer) error {
	info := Info(manifest)

	for _, layer := range manifest.Layers {
		title := layer.Annotations[v1.AnnotationTitle]
		if title == info.BinPath || filepath.Base(title) == filepath.Base(info.BinPath) {
			rc, err := fetcher.Fetch(ctx, layer)
			if err != nil {
				return err
			}
			defer rc.Close()

			_, err = io.Copy(w, rc)
			return err
		}
	}

	for _, layer := range manifest.Layers {
		if !strings.Contains(layer.MediaType, "tar") {
			continue
		}

		found, err := writeBinFromTar(ctx, fetcher, layer, info.BinPath, w)
		if err != nil {
			return err
		}

		if found {
			return nil
		}
	}

	return fmt.Errorf("no binary %s found in layers", info.BinPath)
}

func writeBinFromTar(
	ctx context.Context, fetcher content.Fetcher, layer v1.Descriptor, binPath string, w io.Writer,
) (bool, error) {
	rc, err := fetcher.Fetch(ctx, layer)
	if err != nil {
		return false, err
	}
	defer rc.Close()

	var reader io.Reader = rc

	if strings.Contains(layer.MediaType, "gzip") {
		gz, gzErr := gzip.NewReader(rc)
		if gzErr != nil {
			return false, gzErr
		}
		defer gz.Close()

		reader = gz
	}

	tr := tar.NewReader(reader)

	for {
		hdr, tarErr := tr.Next()
		if tarErr == io.EOF {
			break
		}

		if tarErr != nil {
			return false, tarErr
		}

		if (hdr.Name == binPath || filepath.Base(hdr.Name) == filepath.Base(binPath)) && hdr.Typeflag == tar.TypeReg {
			_, err = io.Copy(w, tr) //nolint:gosec // trusted OCI registry content
			return true, err
		}
	}

	return false, nil
}
