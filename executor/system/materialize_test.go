//nolint:testpackage // direct internal access
package system

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/util"
	"github.com/google/uuid"
	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/build"
	agentoci "github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
)

// ensure billy.Filesystem stays imported (used by helpers below).
var _ billy.Filesystem = memfs.New()

// buildMaterializeFixture stages an in-memory Agentfile (one CONTEXT,
// one ADD, one BIN) in a real OCI memory store and returns the store
// plus the ref that resolves to it. The same pattern used by
// store/load_test.go — we're reusing the build pipeline as a fixture
// so materialize sees a realistic manifest without touching a
// registry or disk.
func buildMaterializeFixture(t *testing.T) (*memory.Store, spec.Reference) {
	t.Helper()

	srcFS := memfs.New()
	f, err := srcFS.Create("data.txt")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if _, e := f.Write([]byte("hello")); e != nil {
		t.Fatalf("fixture: %v", e)
	}
	_ = f.Close()

	af := &spec.Agentfile{
		Syntax: spec.DefaultSyntax,
		Agent: &spec.Agent{
			From:    "scratch",
			Name:    "matz",
			Runtime: "ghcr.io/openotters/runtime:latest",
			Contexts: []*spec.Context{
				{Name: "SOUL", Content: "Answer concisely."},
			},
			Adds: []*spec.Add{
				{Src: "data.txt", Dst: "/data/data.txt", Description: "sample"},
			},
			Bins: []*spec.Bin{
				{Name: "jq", Image: "ghcr.io/openotters/tools/jq:latest", Description: "JSON"},
			},
		},
	}

	s := memory.New()
	ref, err := build.Build(context.Background(), af, srcFS, s)
	if err != nil {
		t.Fatalf("build.Build: %v", err)
	}

	return s, ref.Reference
}

func TestWorkspaceMaterialize_EndToEnd(t *testing.T) {
	t.Parallel()

	store, ref := buildMaterializeFixture(t)

	// Capture puller calls so we can assert the runtime + each BIN was
	// requested. NoopPuller writes "#!/bin/sh\n" which is enough for
	// the binary file to exist on-chroot.
	var pulled []string
	puller := agentoci.Puller(func(_ context.Context, r spec.Reference, w io.Writer) error {
		pulled = append(pulled, r.String())
		_, err := w.Write([]byte("#!/bin/sh\n"))
		return err
	})

	chroot := memfs.New()
	hostFS := memfs.New()

	w := &workspace{
		store:     store,
		ref:       ref,
		ociPuller: puller,
		hostFS:    hostFS,
	}

	rt, err := w.materialize(context.Background(), chroot, uuid.New(), "127.0.0.1:42")
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}

	// Runtime descriptor assembled from the Agentfile.
	if rt.Name != "matz" || rt.Model != "" || rt.Addr != "127.0.0.1:42" {
		t.Fatalf("unexpected runtime: %+v", rt.ResolvedConfig)
	}

	if len(rt.Tools) != 1 || rt.Tools[0].Name != "jq" {
		t.Fatalf("unexpected Tools: %+v", rt.Tools)
	}

	// Workspace filesystem layout — FHS dirs created.
	for _, dir := range fhsDirs {
		if _, e := chroot.Stat(dir); e != nil {
			t.Errorf("fhs dir %q missing: %v", dir, e)
		}
	}

	// Runtime binary landed at usr/local/bin/runtime.
	if _, e := chroot.Stat(RuntimeBin); e != nil {
		t.Errorf("runtime binary missing: %v", e)
	}

	// Tool binary landed at usr/bin/jq.
	if _, e := chroot.Stat("usr/bin/jq"); e != nil {
		t.Errorf("jq binary missing: %v", e)
	}

	// ADD file materialised into etc/data/data.txt — basename only,
	// matching the existing workspace.extractLayers contract.
	if _, e := chroot.Stat("etc/data/data.txt"); e != nil {
		t.Errorf("data file missing: %v", e)
	}

	// AGENT.md + WORKSPACE.md written by materialize's tail.
	if _, e := chroot.Stat("etc/context/AGENT.md"); e != nil {
		t.Errorf("AGENT.md missing: %v", e)
	}
	if _, e := chroot.Stat("etc/context/WORKSPACE.md"); e != nil {
		t.Errorf("WORKSPACE.md missing: %v", e)
	}

	// Puller was invoked for runtime + jq.
	joined := strings.Join(pulled, ",")
	for _, want := range []string{"ghcr.io/openotters/runtime:latest", "ghcr.io/openotters/tools/jq:latest"} {
		if !strings.Contains(joined, want) {
			t.Errorf("puller did not see %q; called with %v", want, pulled)
		}
	}
}

func TestInstallRuntime_LocalRuntimeCopiesThroughHostFS(t *testing.T) {
	t.Parallel()

	hostFS := memfs.New()

	// Stage a "runtime binary" on hostFS at an absolute path; copyLocalFile
	// must be able to read through hostFS, not via os.Open.
	f, err := hostFS.Create("/opt/runtime-bin")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	_, _ = f.Write([]byte("#!/bin/sh\necho runtime\n"))
	_ = f.Close()

	chroot := memfs.New()

	w := &workspace{
		hostFS:       hostFS,
		localRuntime: "/opt/runtime-bin",
	}

	// af.Agent.Runtime doesn't matter when localRuntime is set — the
	// local branch is preferred.
	af := &spec.Agentfile{Agent: &spec.Agent{Runtime: "ghcr.io/ignored"}}

	if e := w.installRuntime(context.Background(), chroot, af); e != nil {
		t.Fatalf("installRuntime: %v", e)
	}

	got, err := util.ReadFile(chroot, RuntimeBin)
	if err != nil {
		t.Fatalf("read chroot runtime: %v", err)
	}

	if !strings.Contains(string(got), "echo runtime") {
		t.Fatalf("chroot runtime content = %q, want copy of /opt/runtime-bin", string(got))
	}
}

// keep io imported (used by Puller signature above).
var _ io.Writer = io.Discard
