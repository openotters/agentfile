//nolint:testpackage // direct internal access
package system

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-billy/v6/util"
	"github.com/google/uuid"
	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/executor"
	agentoci "github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
)

// --- setRan / getRan ----------------------------------------------------

func TestSetRan_GetRan_RoundTrip(t *testing.T) {
	t.Parallel()

	a := NewAgent(uuid.New(), memfs.New())

	if a.getRan() != nil {
		t.Fatalf("fresh Agent should have nil ran channel")
	}

	ch := make(chan struct{})
	a.setRan(ch)

	if got := a.getRan(); got != ch {
		t.Fatalf("getRan = %v, want the channel we set", got)
	}
}

// --- markInitialized ----------------------------------------------------

func TestMarkInitialized_PopulatesRuntimeAndAddr(t *testing.T) {
	t.Parallel()

	a := NewAgent(uuid.New(), memfs.New()) // addr empty by default

	rt := &executor.Runtime{
		ResolvedConfig: executor.ResolvedConfig{Model: "m", Addr: "127.0.0.1:4321"},
	}

	a.markInitialized(rt)

	if a.Runtime() != rt {
		t.Fatalf("Runtime() did not return the marked-initialized runtime")
	}

	if a.Addr() != "127.0.0.1:4321" {
		t.Fatalf("Addr() = %q, want runtime's Addr to be adopted when agent had none", a.Addr())
	}

	if a.cmdFn == nil {
		t.Fatal("cmdFn should be built by markInitialized")
	}
}

func TestMarkInitialized_KeepsExistingAddr(t *testing.T) {
	t.Parallel()

	// Agent already has an addr (set via WithAddr) — markInitialized must
	// not clobber it with the runtime's addr. This covers the branch
	// guarding `a.addr == ""`.
	a := NewAgent(uuid.New(), memfs.New(), WithAddr("127.0.0.1:1111"))

	rt := &executor.Runtime{
		ResolvedConfig: executor.ResolvedConfig{Model: "m", Addr: "127.0.0.1:9999"},
	}

	a.markInitialized(rt)

	if a.Addr() != "127.0.0.1:1111" {
		t.Fatalf("Addr() = %q, markInitialized overwrote a pre-set addr", a.Addr())
	}
}

func TestMarkInitialized_IsIdempotent(t *testing.T) {
	t.Parallel()

	a := NewAgent(uuid.New(), memfs.New())

	first := &executor.Runtime{ResolvedConfig: executor.ResolvedConfig{Model: "first"}}
	second := &executor.Runtime{ResolvedConfig: executor.ResolvedConfig{Model: "second"}}

	a.markInitialized(first)
	a.markInitialized(second) // must not replace first — initOnce guards

	if a.Runtime().Model != "first" {
		t.Fatalf("second markInitialized overrode first; got Model=%q", a.Runtime().Model)
	}
}

// --- Prepare error path -------------------------------------------------

func TestPrepare_MaterializeErrorSetsPullError(t *testing.T) {
	t.Parallel()

	// Empty OCI store + a ref that doesn't exist → afstore.Load fails,
	// materialize wraps the failure with ErrPull, Prepare transitions to
	// StatusPullError and returns the error. This covers the happy
	// branch of the ErrPull discriminator in Prepare.
	a := NewAgent(uuid.New(), memfs.New(),
		WithStore(memory.New()),
		WithReference(spec.Reference{Name: "missing", Tag: "latest"}),
	)

	err := a.Prepare(context.Background())
	if err == nil {
		t.Fatal("Prepare with empty store = nil, want error")
	}

	if got := a.Status(); got != executor.StatusPullError {
		t.Fatalf("Status after failed Prepare = %v, want StatusPullError", got)
	}
}

func TestPrepare_ModelResolverErrorSetsModelError(t *testing.T) {
	t.Parallel()

	// Build a real image fixture so afstore.Load + extractLayers succeed
	// and the failure surfaces from the model resolver — the only branch
	// that should set StatusModelError. Mirrors the ErrPull discriminator
	// test above.
	store, ref := buildMaterializeFixture(t)

	puller := agentoci.Puller(func(_ context.Context, _ spec.Reference, w io.Writer) error {
		_, err := w.Write([]byte("#!/bin/sh\n"))

		return err
	})

	resolverErr := fmt.Errorf("provider %q not configured", "anthropic")
	resolver := func(string) (string, string, error) { return "", "", resolverErr }

	a := NewAgent(uuid.New(), memfs.New(),
		WithStore(store),
		WithReference(ref),
		WithAgentPuller(puller),
		WithModelResolver(resolver),
		WithOverrides(spec.WithModel("anthropic/claude-sonnet-4-20250514")),
	)

	err := a.Prepare(context.Background())
	if err == nil {
		t.Fatal("Prepare with failing resolver = nil, want error")
	}

	if !errors.Is(err, ErrModel) {
		t.Fatalf("Prepare err = %v, want errors.Is ErrModel", err)
	}

	if got := a.Status(); got != executor.StatusModelError {
		t.Fatalf("Status after failed Prepare = %v, want StatusModelError", got)
	}
}

func TestPrepare_IsIdempotent(t *testing.T) {
	t.Parallel()

	a := NewAgent(uuid.New(), memfs.New(),
		WithStore(memory.New()),
		WithReference(spec.Reference{Name: "missing", Tag: "latest"}),
	)

	err1 := a.Prepare(context.Background())
	err2 := a.Prepare(context.Background())

	if err1 == nil || err2 == nil {
		t.Fatalf("expected both calls to return an error; got %v / %v", err1, err2)
	}

	// Failures aren't cached — but the failure mode is deterministic
	// (empty store + missing ref = same afstore.Load error every call),
	// so two attempts produce the same error string.
	if err1.Error() != err2.Error() {
		t.Fatalf("repeat Prepare returned different errors: %v / %v", err1, err2)
	}
}

func TestPrepare_RetriesAfterModelError(t *testing.T) {
	t.Parallel()

	// Failed inits used to be cached forever via sync.Once; this test
	// pins the new contract that a corrected providers.yaml unsticks
	// the agent on the next Prepare.
	store, ref := buildMaterializeFixture(t)

	puller := agentoci.Puller(func(_ context.Context, _ spec.Reference, w io.Writer) error {
		_, err := w.Write([]byte("#!/bin/sh\n"))

		return err
	})

	var current atomic.Pointer[func(string) (string, string, error)]

	failing := func(string) (string, string, error) {
		return "", "", fmt.Errorf("provider %q not configured", "anthropic")
	}
	current.Store(&failing)

	resolver := func(m string) (string, string, error) { return (*current.Load())(m) }

	a := NewAgent(uuid.New(), memfs.New(),
		WithStore(store),
		WithReference(ref),
		WithAgentPuller(puller),
		WithModelResolver(resolver),
		WithOverrides(spec.WithModel("anthropic/claude-sonnet-4-20250514")),
	)

	if err := a.Prepare(context.Background()); err == nil || !errors.Is(err, ErrModel) {
		t.Fatalf("first Prepare = %v, want ErrModel", err)
	}

	if got := a.Status(); got != executor.StatusModelError {
		t.Fatalf("status after first Prepare = %v, want StatusModelError", got)
	}

	// Swap to a working resolver and retry — must materialize this time.
	working := func(string) (string, string, error) {
		return "https://api.example.com", "key-fixed", nil
	}
	current.Store(&working)

	if err := a.Prepare(context.Background()); err != nil {
		t.Fatalf("second Prepare after swap = %v, want nil", err)
	}

	if a.rt == nil || a.cmdFn == nil {
		t.Fatal("Prepare succeeded but rt / cmdFn unset")
	}

	if a.rt.APIKey != "key-fixed" {
		t.Fatalf("rt.APIKey = %q, want key-fixed", a.rt.APIKey)
	}
}

// --- Start re-resolve --------------------------------------------------

func TestReResolveCredentials_UpdatesRTOnSuccess(t *testing.T) {
	t.Parallel()

	store, ref := buildMaterializeFixture(t)

	puller := agentoci.Puller(func(_ context.Context, _ spec.Reference, w io.Writer) error {
		_, err := w.Write([]byte("#!/bin/sh\n"))

		return err
	})

	var current atomic.Pointer[func(string) (string, string, error)]

	v1 := func(string) (string, string, error) {
		return "https://api.v1.example.com", "key-v1", nil
	}
	current.Store(&v1)

	resolver := func(m string) (string, string, error) { return (*current.Load())(m) }

	a := NewAgent(uuid.New(), memfs.New(),
		WithStore(store),
		WithReference(ref),
		WithAgentPuller(puller),
		WithModelResolver(resolver),
		WithOverrides(spec.WithModel("anthropic/claude-sonnet-4-20250514")),
	)

	if err := a.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	if a.rt.APIKey != "key-v1" {
		t.Fatalf("post-Prepare APIKey = %q, want key-v1", a.rt.APIKey)
	}

	v2 := func(string) (string, string, error) {
		return "https://api.v2.example.com", "key-v2", nil
	}
	current.Store(&v2)

	if err := a.reresolveCredentials(); err != nil {
		t.Fatalf("reresolveCredentials: %v", err)
	}

	if a.rt.APIKey != "key-v2" || a.rt.APIBase != "https://api.v2.example.com" {
		t.Fatalf("post-reresolve rt = (%q, %q), want (https://api.v2.example.com, key-v2)",
			a.rt.APIBase, a.rt.APIKey)
	}
}

func TestStart_FailsWithModelErrorOnReResolveFailure(t *testing.T) {
	t.Parallel()

	store, ref := buildMaterializeFixture(t)

	puller := agentoci.Puller(func(_ context.Context, _ spec.Reference, w io.Writer) error {
		_, err := w.Write([]byte("#!/bin/sh\n"))

		return err
	})

	var current atomic.Pointer[func(string) (string, string, error)]

	working := func(string) (string, string, error) {
		return "https://api.example.com", "key-original", nil
	}
	current.Store(&working)

	resolver := func(m string) (string, string, error) { return (*current.Load())(m) }

	a := NewAgent(uuid.New(), memfs.New(),
		WithStore(store),
		WithReference(ref),
		WithAgentPuller(puller),
		WithModelResolver(resolver),
		WithOverrides(spec.WithModel("anthropic/claude-sonnet-4-20250514")),
	)

	if err := a.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	// Status hasn't transitioned to Stopped yet (no Run was issued), so
	// fake the lifecycle by setting it manually — Start's switch lets
	// StatusInitError / Created through, so any startable status works.
	a.status.Set(executor.StatusStopped)

	failing := func(string) (string, string, error) {
		return "", "", fmt.Errorf("provider %q not configured", "anthropic")
	}
	current.Store(&failing)

	err := a.Start(context.Background())
	if err == nil {
		t.Fatal("Start with failing resolver = nil, want error")
	}

	if !errors.Is(err, ErrModel) {
		t.Fatalf("Start err = %v, want errors.Is ErrModel", err)
	}

	if got := a.Status(); got != executor.StatusModelError {
		t.Fatalf("status after Start = %v, want StatusModelError", got)
	}

	// Original credentials must remain — no torn write.
	if a.rt.APIKey != "key-original" || a.rt.APIBase != "https://api.example.com" {
		t.Fatalf("rt mutated on failure: APIBase=%q APIKey=%q", a.rt.APIBase, a.rt.APIKey)
	}
}

// --- Stop ---------------------------------------------------------------

func TestStop_NotRunningIsNoop(t *testing.T) {
	t.Parallel()

	a := NewAgent(uuid.New(), memfs.New())

	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("Stop on non-running agent = %v, want nil", err)
	}
}

// --- Remove -------------------------------------------------------------

func TestRemove_NilFilesystem(t *testing.T) {
	t.Parallel()

	// An Agent constructed without a filesystem (shouldn't happen in
	// production, but defensive: Remove should transition status to
	// StatusRemoved and return nil rather than panic on a nil fs).
	a := &Agent{status: executor.NewStatusTracker()}

	if err := a.Remove(context.Background()); err != nil {
		t.Fatalf("Remove(nil fs) = %v, want nil", err)
	}

	if got := a.Status(); got != executor.StatusRemoved {
		t.Fatalf("Status = %v, want StatusRemoved", got)
	}
}

func TestRemove_WipesFilesystem(t *testing.T) {
	t.Parallel()

	// Use a disk-backed chroot so util.RemoveAll(fs, ".") actually
	// clears contents (memfs rejects base-dir removal). Matches the
	// production layout where Provider.CreateWithOptions calls
	// a.fs.Chroot(id.String()) on an osfs root.
	root := osfs.New(t.TempDir())
	chroot, err := root.Chroot("agent")
	if err != nil {
		t.Fatalf("chroot: %v", err)
	}

	stageFile(t, chroot, "etc/context/AGENT.md", "hello")
	stageFile(t, chroot, "workspace/notes.txt", "scratch")

	a := NewAgent(uuid.New(), chroot)

	if e := a.Remove(context.Background()); e != nil {
		t.Fatalf("Remove: %v", e)
	}

	if got := a.Status(); got != executor.StatusRemoved {
		t.Fatalf("Status = %v, want StatusRemoved", got)
	}

	// Both staged files are gone.
	if _, e := chroot.Stat(filepath.Join("etc", "context", "AGENT.md")); e == nil {
		t.Fatal("AGENT.md still present after Remove")
	}

	if _, e := chroot.Stat(filepath.Join("workspace", "notes.txt")); e == nil {
		t.Fatal("notes.txt still present after Remove")
	}
}

func TestRemove_CtxCancelled(t *testing.T) {
	t.Parallel()

	a := NewAgent(uuid.New(), memfs.New())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	err := a.Remove(ctx)
	if err == nil {
		t.Fatal("Remove(cancelled ctx) = nil, want ctx.Err()")
	}

	// Status moved to Removing before the ctx check — a partial transition
	// is the documented behaviour (we got past the closeClient step but
	// bailed before the filesystem wipe).
	if got := a.Status(); got != executor.StatusRemoving {
		t.Fatalf("Status = %v, want StatusRemoving after aborted Remove", got)
	}
}

// --- Start preconditions ------------------------------------------------

func TestStart_RefusesRunningAgent(t *testing.T) {
	t.Parallel()

	a := NewAgent(uuid.New(), memfs.New())
	a.status.Set(executor.StatusRunning)

	err := a.Start(context.Background())
	if err == nil || err.Error() != "agent already running" {
		t.Fatalf("Start(running) err = %v, want 'agent already running'", err)
	}
}

func TestStart_RefusesRemovedAgent(t *testing.T) {
	t.Parallel()

	for _, s := range []executor.Status{executor.StatusRemoving, executor.StatusRemoved} {
		s := s
		t.Run(s.String(), func(t *testing.T) {
			t.Parallel()

			a := NewAgent(uuid.New(), memfs.New())
			a.status.Set(s)

			err := a.Start(context.Background())
			if err == nil || err.Error() != "agent removed" {
				t.Fatalf("Start(%v) err = %v, want 'agent removed'", s, err)
			}
		})
	}
}

// --- helpers ------------------------------------------------------------

// stageFile writes body to path on fs; used to give Remove something to
// wipe. util.WriteFile creates parent dirs as needed via billy.
func stageFile(t *testing.T, fs billy.Filesystem, path, body string) {
	t.Helper()

	if err := util.WriteFile(fs, path, []byte(body), 0o644); err != nil {
		t.Fatalf("stage %s: %v", path, err)
	}
}
