package export_test

import (
	"context"
	"testing"

	billy "github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/openotters/agentfile/build"
	"github.com/openotters/agentfile/export"
	"github.com/openotters/agentfile/spec"
	"oras.land/oras-go/v2/content/memory"
)

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

func TestExportImport_Roundtrip(t *testing.T) {
	t.Parallel()

	src := memfs.New()
	if err := writeTestFile(src, "data.json", `{"key":"value"}`); err != nil {
		t.Fatal(err)
	}

	af := &spec.Agentfile{
		Syntax: "openotters/agentfile:1",
		Agent: &spec.Agent{
			From:    "scratch",
			Name:    "roundtrip-test",
			Runtime: "ghcr.io/openotters/runtime:latest",
			Model:   "anthropic/claude-haiku-4-5-20251001",
			Contexts: []*spec.Context{
				{Name: "SOUL", Content: "You are a test agent."},
			},
			Bins: []*spec.Bin{
				{Name: "wget", Image: "ghcr.io/openotters/tools/wget:latest", Description: "Fetch URL"},
			},
			Adds: []*spec.Add{
				{Src: "data.json", Dst: "/data/data.json", Description: "Test data"},
			},
			Labels: map[string]string{"description": "Roundtrip test agent"},
			Args:   map[string]string{},
		},
	}

	store := memory.New()

	buildDigest, err := build.Build(context.Background(), af, src, store)
	if err != nil {
		t.Fatalf("build error: %v", err)
	}

	exported, err := export.Export(store)
	if err != nil {
		t.Fatalf("export error: %v", err)
	}

	if len(exported) == 0 {
		t.Fatal("exported data is empty")
	}

	importedStore, digest, err := export.Import(exported)
	if err != nil {
		t.Fatalf("import error: %v", err)
	}

	if digest == "" {
		t.Error("import returned empty digest")
	}

	if digest != buildDigest.String() {
		t.Errorf("digest mismatch: build=%s import=%s", buildDigest, digest)
	}

	desc, err := importedStore.Resolve(context.Background(), "latest")
	if err != nil {
		t.Fatalf("resolving latest from imported store: %v", err)
	}

	if desc.Digest.String() != buildDigest.String() {
		t.Errorf("store digest = %s, want %s", desc.Digest.String(), buildDigest)
	}
}
