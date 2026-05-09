package build_test

import (
	"context"
	"testing"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/build"
	"github.com/openotters/agentfile/internal"
	"github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
	afstore "github.com/openotters/agentfile/store"
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

func newTestAgentfile(t *testing.T) (*spec.Agentfile, billy.Filesystem) {
	t.Helper()

	src := memfs.New()
	if err := writeTestFile(src, "data.json", `{"key":"value"}`); err != nil {
		t.Fatal(err)
	}

	af := &spec.Agentfile{
		Syntax: "openotters/agentfile:1",
		Agent: &spec.Agent{
			From:    "scratch",
			Name:    "test-agent",
			Runtime: "ghcr.io/openotters/runtime:latest",
			Model:   "anthropic/claude-haiku-4-5-20251001",
			Contexts: []*spec.Context{
				{Name: "SOUL", Description: "Core instructions", Content: "You are a test agent."},
			},
			Configs: []*spec.Config{
				{Key: "max-tokens", Value: "1024", Description: "Max tokens"},
			},
			Bins: []*spec.Bin{
				{Name: "wget", Image: "ghcr.io/openotters/tools/wget:latest", Description: "Fetch URL"},
			},
			Adds: []*spec.Add{
				{Src: "data.json", Dst: "/data/data.json", Description: "Test data"},
			},
			Envs: []*spec.Env{
				{Key: "NODE_ENV", Value: "production", Description: "Application environment"},
			},
			Labels: map[string]string{"description": "A test agent"},
			Args:   map[string]string{},
		},
	}

	return af, src
}

func TestBuild(t *testing.T) {
	t.Parallel()

	af, src := newTestAgentfile(t)
	store := memory.New()

	digest, err := build.Build(context.Background(), af, src, store)
	if err != nil {
		t.Fatalf("build error: %v", err)
	}

	if digest == nil {
		t.Fatal("nil digest")
	}
}

func TestBuildPushPull_Roundtrip(t *testing.T) {
	t.Parallel()

	af, src := newTestAgentfile(t)
	store := memory.New()

	buildRef, err := build.Build(context.Background(), af, src, store)
	if err != nil {
		t.Fatalf("build error: %v", err)
	}

	reg := internal.New()
	defer reg.Close()

	ref := spec.ParseReference(reg.Host() + "/test/agent:v1")

	repo, err := oci.NewRemoteRepository(ref, oci.WithPlainHTTP)
	if err != nil {
		t.Fatalf("repo error: %v", err)
	}

	// Push using build digest as source ref
	_, err = oras.Copy(context.Background(), store, buildRef.Digest.String(), repo, "v1", oras.DefaultCopyOptions)
	if err != nil {
		t.Fatalf("push error: %v", err)
	}

	// Pull
	pulledStore := memory.New()

	desc, err := oras.Copy(context.Background(), repo, "v1", pulledStore, "v1", oras.DefaultCopyOptions)
	if err != nil {
		t.Fatalf("pull error: %v", err)
	}

	// Tag with a proper reference so Load can find it.
	if tagErr := pulledStore.Tag(context.Background(), desc, "roundtrip-test:v1"); tagErr != nil {
		t.Fatalf("tag error: %v", tagErr)
	}

	_, pulled, err := afstore.Load(context.Background(), pulledStore, spec.ParseReference("roundtrip-test:v1"))
	if err != nil {
		t.Fatalf("load error: %v", err)
	}

	if pulled.Agent.Name != af.Agent.Name {
		t.Errorf("name = %q, want %q", pulled.Agent.Name, af.Agent.Name)
	}

	if pulled.Agent.Model != af.Agent.Model {
		t.Errorf("model = %q, want %q", pulled.Agent.Model, af.Agent.Model)
	}

	if pulled.Agent.From != af.Agent.From {
		t.Errorf("from = %q, want %q", pulled.Agent.From, af.Agent.From)
	}

	if len(pulled.Agent.Contexts) != len(af.Agent.Contexts) {
		t.Fatalf("contexts = %d, want %d", len(pulled.Agent.Contexts), len(af.Agent.Contexts))
	}

	if pulled.Agent.Contexts[0].Content != af.Agent.Contexts[0].Content {
		t.Errorf("context content = %q", pulled.Agent.Contexts[0].Content)
	}

	if len(pulled.Agent.Configs) != 1 {
		t.Fatalf("configs = %d, want 1", len(pulled.Agent.Configs))
	}

	if pulled.Agent.Configs[0].Key != "max-tokens" || pulled.Agent.Configs[0].Value != "1024" {
		t.Errorf("config = %s=%s", pulled.Agent.Configs[0].Key, pulled.Agent.Configs[0].Value)
	}

	if pulled.Agent.Configs[0].Description != "Max tokens" {
		t.Errorf("config description = %q", pulled.Agent.Configs[0].Description)
	}

	if len(pulled.Agent.Bins) != 1 || pulled.Agent.Bins[0].Name != "wget" {
		t.Errorf("tools = %v", pulled.Agent.Bins)
	}

	if len(pulled.Agent.Adds) != 1 || pulled.Agent.Adds[0].Dst != "/data/data.json" {
		t.Errorf("adds = %v", pulled.Agent.Adds)
	}

	if pulled.Agent.Labels["description"] != "A test agent" {
		t.Errorf("labels = %v", pulled.Agent.Labels)
	}

	if len(pulled.Agent.Envs) != 1 {
		t.Fatalf("envs = %d, want 1", len(pulled.Agent.Envs))
	}

	got := pulled.Agent.Envs[0]
	if got.Key != "NODE_ENV" || got.Value != "production" || got.Description != "Application environment" {
		t.Errorf("env[0] = {%q, %q, %q}", got.Key, got.Value, got.Description)
	}
}
