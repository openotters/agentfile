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
	"github.com/openotters/agentfile/spec"
	afstore "github.com/openotters/agentfile/store"
	"gopkg.in/yaml.v3"
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
	manifest, err := afstore.Manifest(store, ref)
	if err != nil {
		return nil, err
	}

	af, err := afstore.Load(store, ref)
	if err != nil {
		return nil, err
	}

	runtimeBinDir := filepath.Join("usr", "local", "bin")

	result := &Result{
		FS:           e.FS,
		ConfigFile:   filepath.Join("etc", "agent.yaml"),
		RuntimeBin:   filepath.Join(runtimeBinDir, "runtime"),
		ContextDir:   filepath.Join("etc", "context"),
		DataDir:      filepath.Join("etc", "data"),
		BinDir:       filepath.Join("usr", "bin"),
		WorkspaceDir: "workspace",
		TmpDir:       "tmp",
		VarLibDir:    filepath.Join("var", "lib"),
	}

	dirs := []string{
		result.ContextDir, result.DataDir, result.BinDir, runtimeBinDir,
		result.WorkspaceDir, result.TmpDir, result.VarLibDir,
	}
	for _, dir := range dirs {
		if mkdirErr := e.FS.MkdirAll(dir, 0o755); mkdirErr != nil {
			return nil, fmt.Errorf("creating %s: %w", dir, mkdirErr)
		}
	}

	for _, layer := range afstore.Layers(manifest, spec.ContextLayerMediaType) {
		title := layer.Annotations[v1.AnnotationTitle]
		if title == "" {
			continue
		}

		data, fetchErr := afstore.FetchLayer(store, layer)
		if fetchErr != nil {
			return nil, fmt.Errorf("fetching context %s: %w", title, fetchErr)
		}

		if writeErr := writeFile(e.FS, filepath.Join(result.ContextDir, title), data); writeErr != nil {
			return nil, fmt.Errorf("writing context %s: %w", title, writeErr)
		}
	}

	for _, layer := range afstore.Layers(manifest, spec.OctetStream) {
		title := layer.Annotations[v1.AnnotationTitle]
		if title == "" {
			continue
		}

		data, fetchErr := afstore.FetchLayer(store, layer)
		if fetchErr != nil {
			return nil, fmt.Errorf("fetching file %s: %w", title, fetchErr)
		}

		dst := filepath.Base(title)
		if writeErr := writeFile(e.FS, filepath.Join(result.DataDir, dst), data); writeErr != nil {
			return nil, fmt.Errorf("writing file %s: %w", dst, writeErr)
		}
	}

	if e.BinPuller != nil {
		if af.Agent.Runtime != "" {
			if pullErr := e.pullBin(ctx, af.Agent.Runtime, result.RuntimeBin); pullErr != nil {
				return nil, fmt.Errorf("pulling runtime: %w", pullErr)
			}
		}

		for _, t := range af.Agent.Bins {
			dest := filepath.Join(result.BinDir, t.Name)
			if pullErr := e.pullBin(ctx, t.Image, dest); pullErr != nil {
				return nil, fmt.Errorf("pulling tool %s: %w", t.Name, pullErr)
			}
		}
	}

	agentMD := GenerateAgentMD(af)
	if writeErr := writeFile(e.FS, filepath.Join(result.ContextDir, "AGENT.md"), []byte(agentMD)); writeErr != nil {
		return nil, fmt.Errorf("writing AGENT.md: %w", writeErr)
	}

	if writeErr := e.writeAgentConfig(af, result); writeErr != nil {
		return nil, fmt.Errorf("writing agent config: %w", writeErr)
	}

	return result, nil
}

// AgentConfig is the spec-level agent configuration written to etc/agent.yaml.
// It describes the materialized agent in a runtime-agnostic format.
// Any runtime that follows the Agentfile spec can consume this file.
type AgentConfig struct {
	Name    string            `yaml:"name"`
	Model   string            `yaml:"model"`
	Configs map[string]string `yaml:"configs,omitempty"`
	Tools   []AgentConfigTool `yaml:"tools,omitempty"`
}

type AgentConfigTool struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	Binary      string `yaml:"binary"`
}

func (e *FileExecutor) writeAgentConfig(af *spec.Agentfile, result *Result) error {
	cfg := AgentConfig{
		Name:  af.Agent.Name,
		Model: af.Agent.Model,
	}

	if len(af.Agent.Configs) > 0 {
		cfg.Configs = make(map[string]string, len(af.Agent.Configs))
		for _, c := range af.Agent.Configs {
			if c.Value != "" {
				cfg.Configs[c.Key] = c.Value
			}
		}
	}

	for _, t := range af.Agent.Bins {
		cfg.Tools = append(cfg.Tools, AgentConfigTool{
			Name:        t.Name,
			Description: t.Description,
			Binary:      filepath.Join(result.BinDir, t.Name),
		})
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	return writeFile(e.FS, result.ConfigFile, data)
}

func (e *FileExecutor) pullBin(ctx context.Context, ref, dest string) error {
	f, err := e.FS.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}

	pullErr := e.BinPuller(ctx, ref, f)

	if closeErr := f.Close(); pullErr == nil {
		pullErr = closeErr
	}

	return pullErr
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
