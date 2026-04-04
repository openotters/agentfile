package executor_test

import (
	"context"
	"io"
	"strings"
	"testing"

	billy "github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/openotters/agentfile/pkg/agentfile"
	"github.com/openotters/agentfile/pkg/agentfile/build"
	"github.com/openotters/agentfile/pkg/agentfile/executor"
	"github.com/openotters/agentfile/pkg/tool"
	"oras.land/oras-go/v2/content/memory"
)

func buildTestStore(t *testing.T) *memory.Store {
	t.Helper()

	src := memfs.New()
	_ = writeTestFile(src, "cities.json", `[{"city":"Lyon"}]`)

	af := agentfile.Agentfile{
		Syntax: "openotters/agentfile:1",
		Agent: &agentfile.Agent{
			From:    "scratch",
			Runtime: "ghcr.io/openotters/runtime:latest",
			Model:   "anthropic/claude-haiku-4-5-20251001",
			Name:    "test-agent",
			Contexts: []*agentfile.Context{
				{Name: "SOUL", Description: "Core instructions", Content: "You are a test agent."},
				{Name: "IDENTITY", Content: "Name: Test Bot"},
			},
			Configs: []*agentfile.Config{
				{Key: "max-tokens", Value: "1024", Description: "Max tokens"},
			},
			Bins: []*agentfile.Bin{
				{Name: "wget", Image: "ghcr.io/openotters/tools/wget:latest", Description: "Fetch URL content"},
				{Name: "jq", Image: "ghcr.io/openotters/tools/jq:latest", Description: "Extract fields from JSON"},
			},
			Adds: []*agentfile.Add{
				{Src: "cities.json", Dst: "/data/cities.json", Description: "Known cities"},
			},
			Labels: map[string]string{"description": "A test agent"},
			Args:   map[string]string{},
		},
	}

	store := memory.New()

	_, err := build.Build(context.Background(), &af, src, store)
	if err != nil {
		t.Fatal(err)
	}

	return store
}

var noopBinPuller executor.Puller = tool.NoopPuller //nolint:gochecknoglobals // test helper

func writeTestFile(fs billy.Filesystem, path string, content string) error {
	f, err := fs.Create(path)
	if err != nil {
		return err
	}

	_, err = f.Write([]byte(content))

	if closeErr := f.Close(); err == nil {
		err = closeErr
	}

	return err
}

func readTestFile(fs billy.Filesystem, path string) (string, error) {
	f, err := fs.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	return string(data), err
}

func TestFileExecutor_CreatesDirectoryTree(t *testing.T) {
	t.Parallel()

	store := memory.New()

	af := agentfile.Agentfile{
		Syntax: "openotters/agentfile:1",
		Agent: &agentfile.Agent{
			From:   "scratch",
			Name:   "minimal",
			Labels: map[string]string{},
			Args:   map[string]string{},
		},
	}

	_, err := build.Build(context.Background(), &af, memfs.New(), store)
	if err != nil {
		t.Fatal(err)
	}

	e := &executor.FileExecutor{FS: memfs.New(), BinPuller: noopBinPuller}
	result, err := e.Execute(context.Background(), store, "latest")
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}

	dirs := []string{
		result.ContextDir, result.DataDir, result.BinDir,
		result.WorkspaceDir, result.TmpDir, result.VarLibDir,
	}
	for _, dir := range dirs {
		info, statErr := result.FS.Stat(dir)
		if statErr != nil {
			t.Errorf("directory %s not created: %v", dir, statErr)
		} else if !info.IsDir() {
			t.Errorf("%s is not a directory", dir)
		}
	}
}

func TestFileExecutor_WritesContextFiles(t *testing.T) {
	t.Parallel()

	store := buildTestStore(t)

	e := &executor.FileExecutor{FS: memfs.New(), BinPuller: noopBinPuller}
	result, err := e.Execute(context.Background(), store, "latest")
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}

	soul, err := readTestFile(result.FS, "etc/context/SOUL.md")
	if err != nil {
		t.Fatalf("reading SOUL.md: %v", err)
	}

	if soul != "You are a test agent." {
		t.Errorf("SOUL.md = %q", soul)
	}

	identity, err := readTestFile(result.FS, "etc/context/IDENTITY.md")
	if err != nil {
		t.Fatalf("reading IDENTITY.md: %v", err)
	}

	if identity != "Name: Test Bot" {
		t.Errorf("IDENTITY.md = %q", identity)
	}
}

func TestFileExecutor_ExtractsDataFiles(t *testing.T) {
	t.Parallel()

	store := buildTestStore(t)

	e := &executor.FileExecutor{FS: memfs.New(), BinPuller: noopBinPuller}
	result, err := e.Execute(context.Background(), store, "latest")
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}

	data, err := readTestFile(result.FS, "etc/data/cities.json")
	if err != nil {
		t.Fatalf("reading cities.json: %v", err)
	}

	if data != `[{"city":"Lyon"}]` {
		t.Errorf("cities.json = %q", data)
	}
}

func TestFileExecutor_PullsBins(t *testing.T) {
	t.Parallel()

	store := buildTestStore(t)

	var pulled []string

	e := &executor.FileExecutor{
		FS: memfs.New(),
		BinPuller: executor.Puller(func(_ context.Context, ref string, w io.Writer) error {
			pulled = append(pulled, ref)
			_, err := w.Write([]byte("fake-bin"))
			return err
		}),
	}

	result, err := e.Execute(context.Background(), store, "latest")
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}

	if len(pulled) != 2 {
		t.Fatalf("pulled = %d, want 2", len(pulled))
	}

	if pulled[0] != "ghcr.io/openotters/tools/wget:latest" {
		t.Errorf("pulled[0] = %q", pulled[0])
	}

	for _, name := range []string{"wget", "jq"} {
		if _, statErr := result.FS.Stat("usr/bin/" + name); statErr != nil {
			t.Errorf("binary %s not found: %v", name, statErr)
		}
	}
}

func TestFileExecutor_GeneratesAgentMD(t *testing.T) {
	t.Parallel()

	store := buildTestStore(t)

	e := &executor.FileExecutor{FS: memfs.New(), BinPuller: noopBinPuller}
	result, err := e.Execute(context.Background(), store, "latest")
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}

	content, err := readTestFile(result.FS, "etc/context/AGENT.md")
	if err != nil {
		t.Fatalf("reading AGENT.md: %v", err)
	}

	if !strings.Contains(content, "# test-agent") {
		t.Error("AGENT.md missing agent name")
	}

	if !strings.Contains(content, "A test agent") {
		t.Error("AGENT.md missing description")
	}

	if !strings.Contains(content, "**wget**") {
		t.Error("AGENT.md missing wget tool")
	}

	if !strings.Contains(content, "cities.json") {
		t.Error("AGENT.md missing data file")
	}

	if !strings.Contains(content, "workspace/") {
		t.Error("AGENT.md missing filesystem section")
	}
}
