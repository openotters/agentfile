package oci_test

import (
	"context"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/build"
	"github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
)

// TestLoadAgentFromSource_TaggedRef pins the contract that a parent
// agent reference with a non-"latest" tag (e.g. "base:v1") resolves
// correctly. Repro for the "resolving manifest: latest:latest: not
// found" bug observed when running a child Agentfile whose FROM
// pointed at a tagged parent.
func TestLoadAgentFromSource_TaggedRef(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	src := memory.New()

	// Build a parent agent into the memory store. build.Build tags it
	// as "<Name>:latest" by default.
	parentAF := &spec.Agentfile{
		Syntax: spec.DefaultSyntax,
		Agent: &spec.Agent{
			From: "scratch",
			Name: "parent",
			Contexts: []*spec.Context{
				{Name: "SOUL", Content: "I am the parent."},
			},
		},
	}

	built, err := build.Build(ctx, parentAF, memfs.New(), src)
	if err != nil {
		t.Fatalf("build.Build: %v", err)
	}

	// Re-tag the manifest under "v1" so the load below has to deal
	// with a non-"latest" tag — same shape the daemon ends up with
	// after `otters image build -t parent:v1`.
	desc, err := src.Resolve(ctx, built.Reference.String())
	if err != nil {
		t.Fatalf("resolving built ref: %v", err)
	}

	if tagErr := src.Tag(ctx, desc, "v1"); tagErr != nil {
		t.Fatalf("tagging v1: %v", tagErr)
	}

	// Act: load using a Reference with Tag != "latest".
	got, err := oci.LoadAgentFromSource(ctx, src, spec.Reference{Name: "parent", Tag: "v1"})
	if err != nil {
		t.Fatalf("LoadAgentFromSource: %v", err)
	}

	// Assert: we got the parent we built.
	if got.Agent == nil {
		t.Fatal("nil Agent")
	}

	if got.Agent.Name != "parent" {
		t.Errorf("Agent.Name = %q, want parent", got.Agent.Name)
	}

	if len(got.Agent.Contexts) != 1 || got.Agent.Contexts[0].Name != "SOUL" {
		t.Errorf("Agent.Contexts = %+v, want one SOUL entry", got.Agent.Contexts)
	}
}
