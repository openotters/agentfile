package tool

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v6"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
)

// ExtractBin fetches the tool binary from fetcher and writes it to fs at dest.
// The binary is identified by info.BinPath matched against layer title annotations,
// with a fallback to tar layer extraction.
func ExtractBin(
	ctx context.Context, fetcher content.Fetcher, manifest v1.Manifest, fs billy.Filesystem, dest string,
) error {
	info := Info(manifest)

	return extractBin(ctx, fetcher, manifest.Layers, fs, info.BinPath, dest)
}

func extractBin(
	ctx context.Context, fetcher content.Fetcher, layers []v1.Descriptor,
	fs billy.Filesystem, binPath string, dest string,
) error {
	for _, layer := range layers {
		title := layer.Annotations[v1.AnnotationTitle]
		if title == binPath || filepath.Base(title) == filepath.Base(binPath) {
			rc, err := fetcher.Fetch(ctx, layer)
			if err != nil {
				return fmt.Errorf("fetching bin: %w", err)
			}
			defer rc.Close()

			data, err := io.ReadAll(rc)
			if err != nil {
				return err
			}

			return writeExecutable(fs, dest, data)
		}
	}

	for _, layer := range layers {
		if !strings.Contains(layer.MediaType, "tar") {
			continue
		}

		found, err := extractBinFromTar(ctx, fetcher, layer, fs, binPath, dest)
		if err != nil {
			return err
		}

		if found {
			return nil
		}
	}

	return fmt.Errorf("no binary %s found in layers", binPath)
}

func extractBinFromTar(
	ctx context.Context, fetcher content.Fetcher, layer v1.Descriptor, fs billy.Filesystem, binPath string, dest string,
) (bool, error) {
	rc, err := fetcher.Fetch(ctx, layer)
	if err != nil {
		return false, fmt.Errorf("fetching layer: %w", err)
	}
	defer rc.Close()

	var reader io.Reader = rc

	if strings.Contains(layer.MediaType, "gzip") {
		gz, gzErr := gzip.NewReader(rc)
		if gzErr != nil {
			return false, fmt.Errorf("decompressing: %w", gzErr)
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
			return false, fmt.Errorf("reading tar: %w", tarErr)
		}

		if (hdr.Name == binPath || filepath.Base(hdr.Name) == filepath.Base(binPath)) && hdr.Typeflag == tar.TypeReg {
			data, readErr := io.ReadAll(tr)
			if readErr != nil {
				return false, fmt.Errorf("reading bin from tar: %w", readErr)
			}

			return true, writeExecutable(fs, dest, data)
		}
	}

	return false, nil
}

func writeExecutable(fs billy.Filesystem, path string, data []byte) error {
	f, err := fs.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}

	_, err = f.Write(data)

	if closeErr := f.Close(); err == nil {
		err = closeErr
	}

	return err
}
