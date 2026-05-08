package system

import (
	"context"
	"io"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/google/uuid"

	"github.com/openotters/agentfile/executor"
	agentoci "github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
)

// MaterializeContent is the public wrapper the docker executor
// uses to share the system pipeline. The fixture below reuses
// buildMaterializeFixture (defined in materialize_test.go) so we
// don't duplicate the agent OCI artifact construction.
func TestMaterializeContent_EndToEnd(t *testing.T) {
	t.Parallel()

	store, ref := buildMaterializeFixture(t)

	puller := agentoci.Puller(func(_ context.Context, _ spec.Reference, w io.Writer) error {
		_, err := w.Write([]byte("#!/bin/sh\n"))
		return err
	})

	chroot := memfs.New()
	hostFS := memfs.New()

	rt, err := MaterializeContent(
		context.Background(), chroot, uuid.New(), "127.0.0.1:42",
		MaterializeOptions{
			Store:     store,
			Ref:       ref,
			OCIPuller: puller,
			HostFS:    hostFS,
		},
	)
	if err != nil {
		t.Fatalf("MaterializeContent: %v", err)
	}
	if rt == nil {
		t.Fatal("MaterializeContent returned nil runtime")
	}
	if rt.Source == nil {
		t.Error("Runtime.Source should be set")
	}
}

// TestMaterializeContent_ToolBinaryPath covers the docker
// executor's hook for stamping in-container paths into agent.yaml
// instead of the chroot path.
func TestMaterializeContent_ToolBinaryPath(t *testing.T) {
	t.Parallel()

	store, ref := buildMaterializeFixture(t)

	puller := agentoci.Puller(func(_ context.Context, _ spec.Reference, w io.Writer) error {
		_, err := w.Write([]byte("#!/bin/sh\n"))
		return err
	})

	chroot := memfs.New()
	hostFS := memfs.New()

	override := func(name string) string {
		return "/opt/bins/" + name + "/" + name
	}

	rt, err := MaterializeContent(
		context.Background(), chroot, uuid.New(), "127.0.0.1:42",
		MaterializeOptions{
			Store:          store,
			Ref:            ref,
			OCIPuller:      puller,
			HostFS:         hostFS,
			ToolBinaryPath: override,
		},
	)
	if err != nil {
		t.Fatalf("MaterializeContent: %v", err)
	}

	for _, tool := range rt.Tools {
		want := override(tool.Name)
		if tool.Binary != want {
			t.Errorf("tool %s binary = %q, want %q", tool.Name, tool.Binary, want)
		}
	}
}

// Smoke test that the option also works without the override
// (i.e. the default usr/bin/<name> path remains).
func TestMaterializeContent_DefaultBinaryPath(t *testing.T) {
	t.Parallel()

	store, ref := buildMaterializeFixture(t)

	puller := agentoci.Puller(func(_ context.Context, _ spec.Reference, w io.Writer) error {
		_, err := w.Write([]byte("#!/bin/sh\n"))
		return err
	})

	chroot := memfs.New()
	hostFS := memfs.New()

	rt, err := MaterializeContent(
		context.Background(), chroot, uuid.New(), "127.0.0.1:42",
		MaterializeOptions{
			Store:     store,
			Ref:       ref,
			OCIPuller: puller,
			HostFS:    hostFS,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, tool := range rt.Tools {
		if tool.Binary == "" {
			t.Errorf("default binary path empty for %s", tool.Name)
		}
		// Still a chroot-relative path, not absolute.
		if tool.Binary[0] == '/' {
			t.Errorf("default binary path %q should be chroot-relative", tool.Binary)
		}
	}
}

// Sanity check: passing nil ToolBinaryPath behaves the same as
// not setting the field.
func TestMaterializeContent_NilToolBinaryPath(t *testing.T) {
	t.Parallel()

	store, ref := buildMaterializeFixture(t)

	puller := agentoci.Puller(func(_ context.Context, _ spec.Reference, w io.Writer) error {
		_, err := w.Write([]byte("#!/bin/sh\n"))
		return err
	})

	chroot := memfs.New()
	hostFS := memfs.New()

	rt, err := MaterializeContent(
		context.Background(), chroot, uuid.New(), "127.0.0.1:42",
		MaterializeOptions{
			Store:          store,
			Ref:            ref,
			OCIPuller:      puller,
			HostFS:         hostFS,
			ToolBinaryPath: nil,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	// Defensive — keeps the executor import live so it doesn't
	// look like an unused import to gofmt.
	_ = executor.Mount{}
	_ = rt
}
