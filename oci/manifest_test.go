//nolint:testpackage // direct internal access
package oci

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"
)

func TestResolveIndex_ExactPlatformMatch(t *testing.T) {
	t.Parallel()

	want := v1.Descriptor{Digest: "sha256:match"}

	index := v1.Index{
		Manifests: []v1.Descriptor{
			{Digest: "sha256:other", Platform: &v1.Platform{OS: "plan9", Architecture: "ppc"}},
			{Digest: want.Digest, Platform: &v1.Platform{OS: runtime.GOOS, Architecture: runtime.GOARCH}},
		},
	}

	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}

	got, ok := resolveIndex(data)
	if !ok {
		t.Fatal("expected a match")
	}

	if got.Digest != want.Digest {
		t.Errorf("digest = %q, want %q", got.Digest, want.Digest)
	}
}

func TestResolveIndex_ArchOnlyFallback(t *testing.T) {
	t.Parallel()

	want := v1.Descriptor{Digest: "sha256:arch-only"}

	index := v1.Index{
		Manifests: []v1.Descriptor{
			{Digest: "sha256:other", Platform: &v1.Platform{OS: "plan9", Architecture: "ppc"}},
			{Digest: want.Digest, Platform: &v1.Platform{OS: "otheros", Architecture: runtime.GOARCH}},
		},
	}

	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}

	got, ok := resolveIndex(data)
	if !ok {
		t.Fatal("expected a match")
	}

	if got.Digest != want.Digest {
		t.Errorf("digest = %q, want %q", got.Digest, want.Digest)
	}
}

func TestResolveIndex_EmptyReturnsFalse(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(v1.Index{})
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := resolveIndex(data); ok {
		t.Fatal("expected no match for empty index")
	}
}

func TestResolveIndex_NonIndexReturnsFalse(t *testing.T) {
	t.Parallel()

	// A v1.Manifest serialized as JSON has no "manifests" array, so
	// resolveIndex should report it is not an index.
	data, err := json.Marshal(v1.Manifest{})
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := resolveIndex(data); ok {
		t.Fatal("manifest should not match as index")
	}
}

func TestResolveManifest_PlainManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := memory.New()

	desc, err := pushJSON(ctx, store, v1.MediaTypeImageManifest, v1.Manifest{
		MediaType: v1.MediaTypeImageManifest,
		Config:    v1.Descriptor{Digest: "sha256:abc"},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := ResolveManifest(ctx, store, desc)
	if err != nil {
		t.Fatalf("ResolveManifest: %v", err)
	}

	if got.Config.Digest != "sha256:abc" {
		t.Errorf("config digest = %q", got.Config.Digest)
	}
}

func TestResolveManifest_FollowsIndex(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := memory.New()

	// Push a child manifest that is the platform match.
	childDesc, err := pushJSON(ctx, store, v1.MediaTypeImageManifest, v1.Manifest{
		MediaType: v1.MediaTypeImageManifest,
		Config:    v1.Descriptor{Digest: "sha256:child-config"},
	})
	if err != nil {
		t.Fatal(err)
	}

	childDesc.Platform = &v1.Platform{OS: runtime.GOOS, Architecture: runtime.GOARCH}

	// Push an index that references the child.
	indexDesc, err := pushJSON(ctx, store, v1.MediaTypeImageIndex, v1.Index{
		MediaType: v1.MediaTypeImageIndex,
		Manifests: []v1.Descriptor{childDesc},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := ResolveManifest(ctx, store, indexDesc)
	if err != nil {
		t.Fatalf("ResolveManifest: %v", err)
	}

	if got.Config.Digest != "sha256:child-config" {
		t.Errorf("config digest = %q, want child manifest config", got.Config.Digest)
	}
}
