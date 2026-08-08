//nolint:testpackage // direct internal access
package oci

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
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

	got, isIndex, err := resolveIndex(data, HostPlatform())
	if err != nil || !isIndex {
		t.Fatalf("expected a match, got isIndex=%v err=%v", isIndex, err)
	}

	if got.Digest != want.Digest {
		t.Errorf("digest = %q, want %q", got.Digest, want.Digest)
	}
}

func TestResolveIndex_NoMatchFailsClosed(t *testing.T) {
	t.Parallel()

	// An index whose entries never match the requested platform must
	// error — never silently fall back to arch-only or first-entry.
	index := v1.Index{
		Manifests: []v1.Descriptor{
			{Digest: "sha256:other", Platform: &v1.Platform{OS: "plan9", Architecture: "ppc"}},
			{Digest: "sha256:arch-only", Platform: &v1.Platform{OS: "otheros", Architecture: runtime.GOARCH}},
		},
	}

	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}

	_, isIndex, err := resolveIndex(data, HostPlatform())
	if !isIndex {
		t.Fatal("expected index detection")
	}

	if err == nil || !strings.Contains(err.Error(), "no manifest for") {
		t.Fatalf("expected fail-closed error naming the platform, got %v", err)
	}

	if !strings.Contains(err.Error(), "plan9/ppc") {
		t.Errorf("error should list available platforms, got %v", err)
	}
}

func TestResolveIndex_TargetPlatformIsParameter(t *testing.T) {
	t.Parallel()

	// The docker executor resolves for linux even on a darwin host —
	// the platform is the caller's choice, not runtime.GOOS.
	want := v1.Descriptor{Digest: "sha256:linux"}

	index := v1.Index{
		Manifests: []v1.Descriptor{
			{Digest: "sha256:darwin", Platform: &v1.Platform{OS: "darwin", Architecture: runtime.GOARCH}},
			{Digest: want.Digest, Platform: &v1.Platform{OS: "linux", Architecture: runtime.GOARCH}},
		},
	}

	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}

	got, isIndex, err := resolveIndex(data, LinuxPlatform())
	if err != nil || !isIndex {
		t.Fatalf("expected a linux match, got isIndex=%v err=%v", isIndex, err)
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

	if _, isIndex, resolveErr := resolveIndex(data, HostPlatform()); isIndex || resolveErr != nil {
		t.Fatalf("expected non-index verdict for empty index, got isIndex=%v err=%v", isIndex, resolveErr)
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

	if _, isIndex, resolveErr := resolveIndex(data, HostPlatform()); isIndex || resolveErr != nil {
		t.Fatalf("manifest should not match as index, got isIndex=%v err=%v", isIndex, resolveErr)
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

	got, err := ResolveManifest(ctx, store, desc, HostPlatform())
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

	got, err := ResolveManifest(ctx, store, indexDesc, HostPlatform())
	if err != nil {
		t.Fatalf("ResolveManifest: %v", err)
	}

	if got.Config.Digest != "sha256:child-config" {
		t.Errorf("config digest = %q, want child manifest config", got.Config.Digest)
	}
}
