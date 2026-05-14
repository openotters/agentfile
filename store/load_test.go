package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/build"
	"github.com/openotters/agentfile/spec"
	"github.com/openotters/agentfile/store"
)

// buildFixture materializes a small Agentfile (with a CONTEXT and an ADD
// layer) into a memory OCI store and returns the store + reference. Shared
// by every load/manifest test in this file so we get realistic multi-layer
// fixtures without resorting to hand-built manifests.
func buildFixture(t *testing.T) (*memory.Store, spec.Reference) {
	t.Helper()

	src := memfs.New()

	f, err := src.Create("data.txt")
	if err != nil {
		t.Fatalf("fixture: create data.txt: %v", err)
	}
	if _, e := f.Write([]byte("hello")); e != nil {
		t.Fatalf("fixture: write data.txt: %v", e)
	}
	_ = f.Close()

	af := &spec.Agentfile{
		Syntax: spec.DefaultSyntax,
		Agent: &spec.Agent{
			From: "scratch",
			Name: "storetest",
			Contexts: []*spec.Context{
				{Name: "SOUL", Content: "You answer in one word."},
			},
			Adds: []*spec.Add{
				{Src: "data.txt", Dst: "/data/data.txt", Description: "sample data"},
			},
		},
	}

	s := memory.New()

	ref, err := build.Build(context.Background(), af, nil, src, s)
	if err != nil {
		t.Fatalf("build.Build: %v", err)
	}

	return s, ref.Reference
}

func TestLoad(t *testing.T) {
	t.Parallel()

	s, ref := buildFixture(t)

	manifest, af, err := store.Load(context.Background(), s, ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if af.Agent == nil || af.Agent.Name != "storetest" {
		t.Fatalf("agent name = %q, want storetest", af.Agent.Name)
	}

	// Load reads Add metadata from the config blob but does NOT fetch the
	// ADD layer — Add.Content (json:"-") should be empty.
	if len(af.Agent.Adds) == 0 {
		t.Fatal("expected an ADD entry on the agentfile")
	}

	if len(af.Agent.Adds[0].Content) != 0 {
		t.Fatalf("Load must not hydrate Add.Content, got %q", string(af.Agent.Adds[0].Content))
	}

	if len(manifest.Layers) == 0 {
		t.Fatal("manifest has no layers")
	}
}

func TestLoad_UnknownRef(t *testing.T) {
	t.Parallel()

	s := memory.New()

	_, _, err := store.Load(context.Background(), s, spec.Reference{Name: "missing", Tag: "latest"})
	if err == nil {
		t.Fatal("Load(missing) = nil, want error")
	}
}

func TestLoadHydrated(t *testing.T) {
	t.Parallel()

	s, ref := buildFixture(t)

	af, err := store.LoadHydrated(context.Background(), s, ref)
	if err != nil {
		t.Fatalf("LoadHydrated: %v", err)
	}

	// Context.Content survives (it's already in the config blob).
	if got := af.Agent.Contexts[0].Content; !strings.Contains(got, "one word") {
		t.Fatalf("Context.Content missing, got %q", got)
	}

	// Add.Content is populated from the /data/data.txt layer — this is the
	// distinguishing behaviour of LoadHydrated vs Load.
	if len(af.Agent.Adds) == 0 || string(af.Agent.Adds[0].Content) != "hello" {
		t.Fatalf("LoadHydrated did not hydrate Add.Content, got %q",
			string(af.Agent.Adds[0].Content))
	}
}

func TestManifestAndLayers(t *testing.T) {
	t.Parallel()

	s, ref := buildFixture(t)

	manifest, err := store.Manifest(context.Background(), s, ref)
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}

	contextLayers := store.Layers(manifest, spec.ContextLayerMediaType)
	if len(contextLayers) == 0 {
		t.Fatalf("expected at least one context layer in %+v", manifest.Layers)
	}

	data, err := store.FetchLayer(context.Background(), s, contextLayers[0])
	if err != nil {
		t.Fatalf("FetchLayer: %v", err)
	}

	if !strings.Contains(string(data), "one word") {
		t.Fatalf("FetchLayer content = %q", string(data))
	}
}

func TestLayers_NoMatch(t *testing.T) {
	t.Parallel()

	s, ref := buildFixture(t)

	manifest, err := store.Manifest(context.Background(), s, ref)
	if err != nil {
		t.Fatal(err)
	}

	// A media type the fixture never emits must produce a nil/empty slice,
	// not an error.
	if got := store.Layers(manifest, "application/vnd.doesnotexist"); len(got) != 0 {
		t.Fatalf("Layers(missing-media-type) = %d entries, want 0", len(got))
	}
}
