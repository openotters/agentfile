package prepare

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/openotters/agentfile/pkg/agentfile"
	"github.com/openotters/agentfile/pkg/agentfile/build"
	"github.com/openotters/agentfile/pkg/agentfile/executor"
	"github.com/openotters/agentfile/pkg/agentfile/parse"
	"github.com/openotters/agentfile/pkg/tool"
	"oras.land/oras-go/v2/content/memory"
)

type Result struct {
	Root      string
	Agentfile *agentfile.Agentfile
}

func Run(agentfilePath string, modelOverride string) (*Result, error) {
	af, err := parse.ParseFile(agentfilePath)
	if err != nil {
		return nil, fmt.Errorf("parsing agentfile: %w", err)
	}

	if modelOverride != "" {
		af.Agent.Model = modelOverride
	}

	srcDir, _ := filepath.Abs(filepath.Dir(agentfilePath))
	store := memory.New()

	_, err = build.Build(context.Background(), af, osfs.New(srcDir), store)
	if err != nil {
		return nil, fmt.Errorf("building: %w", err)
	}

	root, err := os.MkdirTemp("", "agentfile-*")
	if err != nil {
		return nil, err
	}

	e := executor.NewFileExecutor(root)
	e.BinPuller = tool.RemotePuller()

	_, err = e.Execute(context.Background(), store, "latest")
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("executing: %w", err)
	}

	return &Result{Root: root, Agentfile: af}, nil
}

func ServeArgs(r *Result, apiKey, apiBase string) []string {
	args := []string{
		"serve",
		"--root", r.Root,
		"--name", r.Agentfile.Agent.Name,
		"--model", r.Agentfile.Agent.Model,
	}

	if apiKey != "" {
		args = append(args, "--api-key", apiKey)
	}

	if apiBase != "" {
		args = append(args, "--api-base", apiBase)
	}

	return args
}
