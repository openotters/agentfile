package tool_test

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/openotters/agentfile/pkg/tool"
	"oras.land/oras-go/v2/content/memory"
)

func TestBuild_Minimal(t *testing.T) {
	t.Parallel()

	src := memfs.New()
	f, _ := src.Create("jq")
	_, _ = f.Write([]byte("fake-binary"))
	_ = f.Close()

	store := memory.New()

	digest, err := tool.Build(context.Background(), tool.BuildOptions{
		Name:    "jq",
		BinPath: "jq",
	}, src, store)
	if err != nil {
		t.Fatalf("build error: %v", err)
	}

	if digest == nil {
		t.Fatal("nil digest")
	}
}

func TestBuild_WithDescriptionAndUsage(t *testing.T) {
	t.Parallel()

	src := memfs.New()
	f, _ := src.Create("jq")
	_, _ = f.Write([]byte("fake-binary"))
	_ = f.Close()

	store := memory.New()

	_, err := tool.Build(context.Background(), tool.BuildOptions{
		Name:        "jq",
		BinPath:     "jq",
		Description: "Extract fields from JSON",
		Usage:       "First line is the jq expression.\nRest is JSON input.",
	}, src, store)
	if err != nil {
		t.Fatalf("build error: %v", err)
	}

	desc, err := store.Resolve(context.Background(), "latest")
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}

	manifest, err := resolveManifest(t, store, desc)
	if err != nil {
		t.Fatalf("manifest error: %v", err)
	}

	info := tool.Info(*manifest)

	if info.BinPath != "jq" {
		t.Errorf("bin = %q, want jq", info.BinPath)
	}

	if info.Description != "Extract fields from JSON" {
		t.Errorf("description = %q", info.Description)
	}

	if info.UsagePath != "USAGE.md" {
		t.Errorf("usage path = %q, want USAGE.md", info.UsagePath)
	}
}

func TestBuildExtract_Roundtrip(t *testing.T) {
	t.Parallel()

	src := memfs.New()
	f, _ := src.Create("jq")
	_, _ = f.Write([]byte("fake-jq-binary"))
	_ = f.Close()

	store := memory.New()

	_, err := tool.Build(context.Background(), tool.BuildOptions{
		Name:        "jq",
		BinPath:     "jq",
		Description: "JSON processor",
		Usage:       "First line is the expression.",
	}, src, store)
	if err != nil {
		t.Fatalf("build error: %v", err)
	}

	desc, err := store.Resolve(context.Background(), "latest")
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}

	manifest, err := resolveManifest(t, store, desc)
	if err != nil {
		t.Fatalf("manifest error: %v", err)
	}

	dst := memfs.New()
	if extractErr := tool.ExtractBin(context.Background(), store, *manifest, dst, "usr/bin/jq"); extractErr != nil {
		t.Fatalf("extract error: %v", extractErr)
	}

	extracted, err := dst.Open("usr/bin/jq")
	if err != nil {
		t.Fatalf("open error: %v", err)
	}

	buf := make([]byte, 100)
	n, _ := extracted.Read(buf)
	_ = extracted.Close()

	if string(buf[:n]) != "fake-jq-binary" {
		t.Errorf("binary content = %q", string(buf[:n]))
	}

	usage, err := tool.FetchUsage(context.Background(), store, *manifest)
	if err != nil {
		t.Fatalf("usage error: %v", err)
	}

	if usage != "First line is the expression." {
		t.Errorf("usage = %q", usage)
	}
}

func resolveManifest(t *testing.T, store *memory.Store, desc v1.Descriptor) (*v1.Manifest, error) {
	t.Helper()

	rc, err := store.Fetch(context.Background(), desc)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}

	var manifest v1.Manifest
	if unmarshalErr := json.Unmarshal(data, &manifest); unmarshalErr != nil {
		return nil, unmarshalErr
	}

	return &manifest, nil
}
