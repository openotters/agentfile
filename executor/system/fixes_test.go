//nolint:testpackage // exercises unexported lifecycle internals
package system

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/google/uuid"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/build"
	"github.com/openotters/agentfile/executor"
	"github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
)

// blockingSpawner hands back a cmd whose Wait blocks until released, so a Run
// can be caught mid-serve.
type blockingSpawner struct {
	cmd     *stubCmd
	release chan struct{}
}

func newBlockingSpawner() *blockingSpawner {
	release := make(chan struct{})

	return &blockingSpawner{
		release: release,
		cmd:     &stubCmd{waitFn: func() error { <-release; return nil }},
	}
}

func (s *blockingSpawner) Command(string, ...string) Cmd { return s.cmd }

// blockingTarget makes the pull phase hang until ctx is cancelled, simulating a
// slow cold-cache pull that a Stop must be able to interrupt.
type blockingTarget struct{ entered chan struct{} }

func (b *blockingTarget) Resolve(ctx context.Context, _ string) (v1.Descriptor, error) {
	close(b.entered)
	<-ctx.Done()

	return v1.Descriptor{}, ctx.Err()
}

func (b *blockingTarget) Fetch(context.Context, v1.Descriptor) (io.ReadCloser, error) {
	return nil, errors.New("unreachable")
}

func (b *blockingTarget) Exists(context.Context, v1.Descriptor) (bool, error) { return false, nil }

func TestAgent_Stop_CancelsDuringPull(t *testing.T) {
	t.Parallel()

	target := &blockingTarget{entered: make(chan struct{})}
	a := NewAgent(uuid.New(), memfs.New(),
		WithStore(target),
		WithReference(spec.ParseReference("ghost/agent:none")),
		WithAgentPuller(oci.NoopPuller()),
		WithAddr("127.0.0.1:0"),
	)

	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(context.Background()) }()

	<-target.entered // pull is now blocked

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()

	if err := a.Stop(stopCtx); err != nil {
		t.Fatalf("Stop during pull = %v, want nil", err)
	}

	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Stop")
	}

	// A deliberate Stop during the pull must settle to Stopped, not Failed —
	// otherwise a supervisor that auto-restarts Failed agents would loop.
	if got := a.Status(); got != executor.StatusStopped {
		t.Errorf("status after Stop-during-pull = %v, want stopped", got)
	}
}

func TestAgent_Run_RejectsConcurrentRun(t *testing.T) {
	t.Parallel()

	store, ref := buildMaterializeFixture(t)
	sp := newBlockingSpawner()

	a := NewAgent(uuid.New(), memfs.New(),
		WithStore(store), WithReference(ref),
		WithAgentPuller(oci.NoopPuller()),
		WithStaticModelResolver("http://localhost:9", "k"),
		WithAddr("127.0.0.1:0"),
		WithSpawner(sp),
	)

	first := make(chan error, 1)
	go func() { first <- a.Run(context.Background()) }()

	waitUntil(t, func() bool { return a.Status() == executor.StatusStarting })

	if err := a.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second Run = %v, want already-running error", err)
	}

	close(sp.release)
	<-first
}

func TestMaterialize_RejectsHostileArtifact(t *testing.T) {
	t.Parallel()

	// build.Build does not validate, so an unsafe NAME reaches the config
	// blob; materialize must reject it before using the name as a path.
	af := &spec.Agentfile{
		Syntax: spec.DefaultSyntax,
		Agent: &spec.Agent{
			From:    "scratch",
			Name:    "../escape",
			Runtime: "ghcr.io/openotters/runtime:latest",
		},
	}

	store := memory.New()

	ref, err := build.Build(context.Background(), af, nil, memfs.New(), store)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	a := NewAgent(uuid.New(), memfs.New(),
		WithStore(store), WithReference(ref.Reference),
		WithAgentPuller(oci.NoopPuller()),
		WithStaticModelResolver("http://localhost:9", "k"),
		WithAddr("127.0.0.1:0"),
	)

	runErr := a.Run(context.Background())
	if runErr == nil || !strings.Contains(runErr.Error(), "invalid artifact") {
		t.Fatalf("Run on hostile artifact = %v, want invalid-artifact rejection", runErr)
	}

	if a.FailureReason() != executor.FailureConfig {
		t.Errorf("failure = %v, want config", a.FailureReason())
	}
}

func TestProvider_LoadRestoresAddress(t *testing.T) {
	t.Parallel()

	store, ref := buildMaterializeFixture(t)

	provider := NewProvider(memfs.New(), func(spec.Reference) oras.ReadOnlyTarget { return store },
		WithHostFS(memfs.New()),
		WithAgentDefaults(WithAgentPuller(oci.NoopPuller()), WithStaticModelResolver("http://x", "k")),
	)

	agent, err := provider.Create(context.Background(), uuid.New(), ref)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if prepErr := agent.Prepare(context.Background()); prepErr != nil {
		t.Fatalf("prepare: %v", prepErr)
	}

	loaded, err := provider.Load(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if len(loaded) != 1 {
		t.Fatalf("loaded %d agents, want 1", len(loaded))
	}

	restored, ok := loaded[0].(*Agent)
	if !ok {
		t.Fatalf("loaded agent is %T, want *Agent", loaded[0])
	}

	if restored.Addr() == "" {
		t.Error("restored agent has no address; Prompt/Probe would fail")
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(2 * time.Millisecond)
	}

	t.Fatal("condition not met within deadline")
}
