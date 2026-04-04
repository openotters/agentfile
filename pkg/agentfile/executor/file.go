package executor

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	billy "github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/osfs"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/openotters/agentfile/pkg/agentfile"
	"github.com/openotters/agentfile/pkg/agentfile/build"
	"github.com/openotters/agentfile/pkg/utils"
	"oras.land/oras-go/v2/content/memory"
)

// Puller writes a binary to w given an OCI image reference.
type Puller func(ctx context.Context, ref string, w io.Writer) error

type FileExecutor struct {
	FS        billy.Filesystem
	BinPuller Puller
}

func NewFileExecutor(root string) *FileExecutor {
	return &FileExecutor{
		FS: osfs.New(root),
	}
}

func (e *FileExecutor) Execute(ctx context.Context, store *memory.Store, ref string) (*Result, error) {
	manifest, err := agentfile.Manifest(store, ref)
	if err != nil {
		return nil, err
	}

	af, err := agentfile.Load(store, ref)
	if err != nil {
		return nil, err
	}

	result := &Result{
		FS:           e.FS,
		ContextDir:   filepath.Join("etc", "context"),
		DataDir:      filepath.Join("etc", "data"),
		BinDir:       filepath.Join("usr", "bin"),
		WorkspaceDir: "workspace",
		TmpDir:       "tmp",
		VarLibDir:    filepath.Join("var", "lib"),
	}

	dirs := []string{
		result.ContextDir, result.DataDir, result.BinDir,
		result.WorkspaceDir, result.TmpDir, result.VarLibDir,
	}
	for _, dir := range dirs {
		if mkdirErr := e.FS.MkdirAll(dir, 0o755); mkdirErr != nil {
			return nil, fmt.Errorf("creating %s: %w", dir, mkdirErr)
		}
	}

	for _, layer := range agentfile.Layers(manifest, build.ContextLayerMediaType) {
		title := layer.Annotations[v1.AnnotationTitle]
		if title == "" {
			continue
		}

		data, fetchErr := agentfile.FetchLayer(store, layer)
		if fetchErr != nil {
			return nil, fmt.Errorf("fetching context %s: %w", title, fetchErr)
		}

		if writeErr := writeFile(e.FS, filepath.Join(result.ContextDir, title), data); writeErr != nil {
			return nil, fmt.Errorf("writing context %s: %w", title, writeErr)
		}
	}

	for _, layer := range agentfile.Layers(manifest, utils.OctetStream) {
		title := layer.Annotations[v1.AnnotationTitle]
		if title == "" {
			continue
		}

		data, fetchErr := agentfile.FetchLayer(store, layer)
		if fetchErr != nil {
			return nil, fmt.Errorf("fetching file %s: %w", title, fetchErr)
		}

		dst := filepath.Base(title)
		if writeErr := writeFile(e.FS, filepath.Join(result.DataDir, dst), data); writeErr != nil {
			return nil, fmt.Errorf("writing file %s: %w", dst, writeErr)
		}
	}

	if e.BinPuller != nil {
		for _, t := range af.Agent.Bins {
			dest := filepath.Join(result.BinDir, t.Name)

			f, createErr := e.FS.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
			if createErr != nil {
				return nil, fmt.Errorf("creating tool %s: %w", t.Name, createErr)
			}

			pullErr := e.BinPuller(ctx, t.Image, f)

			if closeErr := f.Close(); pullErr == nil {
				pullErr = closeErr
			}

			if pullErr != nil {
				return nil, fmt.Errorf("pulling tool %s: %w", t.Name, pullErr)
			}
		}
	}

	agentMD := GenerateAgentMD(af)
	if writeErr := writeFile(e.FS, filepath.Join(result.ContextDir, "AGENT.md"), []byte(agentMD)); writeErr != nil {
		return nil, fmt.Errorf("writing AGENT.md: %w", writeErr)
	}

	return result, nil
}

func writeFile(bfs billy.Filesystem, path string, data []byte) error {
	f, err := bfs.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}

	_, err = f.Write(data)

	if closeErr := f.Close(); err == nil {
		err = closeErr
	}

	return err
}
