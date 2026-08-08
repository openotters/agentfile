//nolint:testpackage // direct internal access to extractUsage
package oci

import (
	"bytes"
	"context"
	"strings"
	"testing"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/internal"
	"github.com/openotters/agentfile/spec"
)

// pushBinImage assembles a complete bin image — config, a tar.gz rootfs
// layer holding the binary, a usage layer — tags it, and returns the
// store. The manifest carries the io.openotters.bin.* annotations.
func pushBinImage(t *testing.T, binBytes []byte, usage string) *memory.Store {
	t.Helper()

	ctx := context.Background()
	store := memory.New()

	cfg, err := pushBlob(ctx, store, v1.MediaTypeImageConfig, "", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}

	rootfs, err := buildTarGz(map[string][]byte{"bin/hello": binBytes})
	if err != nil {
		t.Fatal(err)
	}

	binLayer, err := pushBlob(ctx, store, v1.MediaTypeImageLayerGzip, "", rootfs)
	if err != nil {
		t.Fatal(err)
	}

	usageLayer, err := pushBlob(ctx, store, "text/markdown", "/USAGE.md", []byte(usage))
	if err != nil {
		t.Fatal(err)
	}

	desc, err := pushJSON(ctx, store, v1.MediaTypeImageManifest, v1.Manifest{
		MediaType: v1.MediaTypeImageManifest,
		Config:    cfg,
		Layers:    []v1.Descriptor{binLayer, usageLayer},
		Annotations: map[string]string{
			spec.AnnotationBinName:  "hello",
			spec.AnnotationBinPath:  "/bin",
			spec.AnnotationBinUsage: "/USAGE.md",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if tagErr := store.Tag(ctx, desc, "v1"); tagErr != nil {
		t.Fatal(tagErr)
	}

	return store
}

func pushToRegistry(t *testing.T, store *memory.Store, reg *internal.Registry, repoPath string) spec.Reference {
	t.Helper()

	ref := spec.ParseReference(reg.Host() + "/" + repoPath + ":v1")

	repo, err := NewRemoteRepository(ref, WithPlainHTTP)
	if err != nil {
		t.Fatal(err)
	}

	if _, copyErr := oras.Copy(context.Background(), store, "v1", repo, "v1", oras.DefaultCopyOptions); copyErr != nil {
		t.Fatalf("push: %v", copyErr)
	}

	return ref
}

func TestRemotePuller_EndToEnd(t *testing.T) {
	t.Parallel()

	binBytes := []byte("\x7fELF hello binary")
	store := pushBinImage(t, binBytes, "run hello")

	reg := internal.New()
	defer reg.Close()

	ref := pushToRegistry(t, store, reg, "tools/hello")

	var out bytes.Buffer
	if err := RemotePuller(HostPlatform(), WithPlainHTTP)(context.Background(), ref, &out); err != nil {
		t.Fatalf("RemotePuller: %v", err)
	}

	if !bytes.Equal(out.Bytes(), binBytes) {
		t.Errorf("pulled bytes = %q, want binary content", out.String())
	}
}

func TestRemoteUsageFetcher_EndToEnd(t *testing.T) {
	t.Parallel()

	store := pushBinImage(t, []byte("bin"), "## Usage\nRun hello with no args.")

	reg := internal.New()
	defer reg.Close()

	ref := pushToRegistry(t, store, reg, "tools/hello-usage")

	got, err := RemoteUsageFetcher(HostPlatform(), WithPlainHTTP)(context.Background(), ref)
	if err != nil {
		t.Fatalf("RemoteUsageFetcher: %v", err)
	}

	if !strings.Contains(got, "Run hello with no args.") {
		t.Errorf("usage = %q", got)
	}
}

func TestExtractUsage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("no annotation means no doc", func(t *testing.T) {
		t.Parallel()

		got, err := extractUsage(ctx, memory.New(), v1.Manifest{})
		if err != nil || got != "" {
			t.Fatalf("got %q, %v; want empty, nil", got, err)
		}
	})

	t.Run("basename match resolves layer", func(t *testing.T) {
		t.Parallel()

		store := memory.New()

		layer, err := pushBlob(ctx, store, "text/markdown", "USAGE.md", []byte("docs"))
		if err != nil {
			t.Fatal(err)
		}

		manifest := v1.Manifest{
			Annotations: map[string]string{spec.AnnotationBinUsage: "/doc/USAGE.md"},
			Layers:      []v1.Descriptor{layer},
		}

		got, err := extractUsage(ctx, store, manifest)
		if err != nil || got != "docs" {
			t.Fatalf("got %q, %v; want docs, nil", got, err)
		}
	})

	t.Run("annotation without matching layer is absent doc", func(t *testing.T) {
		t.Parallel()

		manifest := v1.Manifest{
			Annotations: map[string]string{spec.AnnotationBinUsage: "/USAGE.md"},
		}

		got, err := extractUsage(ctx, memory.New(), manifest)
		if err != nil || got != "" {
			t.Fatalf("got %q, %v; want empty, nil", got, err)
		}
	})
}
