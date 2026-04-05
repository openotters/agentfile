package oci

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
)

func NoopPuller(_ context.Context, _ string, w io.Writer) error {
	_, err := w.Write([]byte("#!/bin/sh\n"))
	return err
}

func RemotePuller(opts ...RemoteRepositoryOption) func(ctx context.Context, ref string, w io.Writer) error {
	return func(ctx context.Context, ref string, w io.Writer) error {
		repo, err := NewRemoteRepository(ref, opts...)
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

		manifest, err := ResolveManifest(ctx, repo, desc)
		if err != nil {
			return err
		}

		return extractBin(ctx, repo, *manifest, w)
	}
}

func extractBin(ctx context.Context, fetcher content.Fetcher, manifest v1.Manifest, w io.Writer) error {
	name := manifest.Annotations["vnd.openotters.bin.name"]
	binPath := manifest.Annotations["vnd.openotters.bin.path"]
	if binPath != "" && name != "" {
		binPath = filepath.Join(binPath, name)
	}

	for _, layer := range manifest.Layers {
		title := layer.Annotations[v1.AnnotationTitle]
		if title == name || title == binPath || filepath.Base(title) == name {
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

		found, err := extractBinFromTar(ctx, fetcher, layer, binPath, w)
		if err != nil {
			return err
		}

		if found {
			return nil
		}
	}

	return fmt.Errorf("no binary found in layers")
}

func extractBinFromTar(
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
