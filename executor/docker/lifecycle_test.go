//nolint:testpackage // exercises unexported lifecycle internals
package docker

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/google/uuid"
	containertypes "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
	"github.com/stretchr/testify/mock"
	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/build"
	"github.com/openotters/agentfile/executor"
	mockdocker "github.com/openotters/agentfile/mocks/docker"
	"github.com/openotters/agentfile/model"
	"github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
)

// buildLifecycleFixture stages a runnable agent artifact (one CONTEXT,
// one BIN) in an OCI memory store, mirroring the system executor's
// materialize fixture.
func buildLifecycleFixture(t *testing.T) (*memory.Store, spec.Reference) {
	t.Helper()

	af := &spec.Agentfile{
		Syntax: spec.DefaultSyntax,
		Agent: &spec.Agent{
			From:    "scratch",
			Name:    "lifecycle",
			Model:   "anthropic/claude-haiku-4-5-20251001",
			Runtime: "ghcr.io/openotters/runtime:latest",
			Contexts: []*spec.Context{
				{Name: "SOUL", Content: "Answer concisely."},
			},
			Bins: []*spec.Bin{
				{Name: "jq", Image: "ghcr.io/openotters/tools/jq:latest", Description: "JSON"},
			},
		},
	}

	s := memory.New()
	ref, err := build.Build(context.Background(), af, nil, memfs.New(), s)
	if err != nil {
		t.Fatalf("build.Build: %v", err)
	}

	return s, ref.Reference
}

func lifecycleDeps(t *testing.T, cli *mockdocker.MockClient) agentDeps {
	t.Helper()

	store, ref := buildLifecycleFixture(t)

	return agentDeps{
		id:           uuid.New(),
		client:       cli,
		baseImage:    DefaultBaseImage,
		ref:          ref,
		fs:           memfs.New(),
		hostFS:       memfs.New(),
		store:        store,
		puller:       oci.NoopPuller(),
		modelResolve: model.StaticResolver("http://localhost:9999", "test-key"),
		hostGRPCPort: "59999",
	}
}

func TestAgent_Run_FullLifecycle(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	a := newAgent(lifecycleDeps(t, cli))

	// Base image cached; runtime + BIN images need a pull.
	cli.EXPECT().ImageInspect(mock.Anything, DefaultBaseImage).
		Return(mobyclient.ImageInspectResult{}, nil).Once()
	cli.EXPECT().ImageInspect(mock.Anything, "ghcr.io/openotters/runtime:latest").
		Return(mobyclient.ImageInspectResult{}, errors.New("no such image")).Once()
	cli.EXPECT().ImagePull(mock.Anything, "ghcr.io/openotters/runtime:latest", mock.Anything).
		Return(newFakeStreamResponse(), nil).Once()
	cli.EXPECT().ImageInspect(mock.Anything, "ghcr.io/openotters/tools/jq:latest").
		Return(mobyclient.ImageInspectResult{}, errors.New("no such image")).Once()
	cli.EXPECT().ImagePull(mock.Anything, "ghcr.io/openotters/tools/jq:latest", mock.Anything).
		Return(newFakeStreamResponse(), nil).Once()

	// No orphan container from a previous run.
	cli.EXPECT().ContainerRemove(mock.Anything, mock.Anything, mock.Anything).
		Return(mobyclient.ContainerRemoveResult{}, notFoundError{}).Once()

	cli.EXPECT().ContainerCreate(mock.Anything, mock.MatchedBy(func(opts mobyclient.ContainerCreateOptions) bool {
		return strings.HasPrefix(opts.Name, "otters-") && opts.Config != nil
	})).Return(mobyclient.ContainerCreateResult{ID: "c-123"}, nil).Once()

	cli.EXPECT().ContainerStart(mock.Anything, "c-123", mock.Anything).
		Return(mobyclient.ContainerStartResult{}, nil).Once()

	// Container "runs" and exits immediately: logs stream EOFs.
	cli.EXPECT().ContainerLogs(mock.Anything, "c-123", mock.Anything).
		Return(io.NopCloser(strings.NewReader("agent output\n")), nil).Once()

	// wait() inspects for the exit code after the log stream ends.
	cli.EXPECT().ContainerInspect(mock.Anything, "c-123", mock.Anything).
		Return(mobyclient.ContainerInspectResult{
			Container: containertypes.InspectResponse{State: &containertypes.State{ExitCode: 0}},
		}, nil).Once()

	if err := a.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := a.Status(); got != executor.StatusStopped {
		t.Errorf("status after clean exit = %v, want stopped", got)
	}

	if rt := a.Runtime(); rt == nil || rt.Name != "lifecycle" {
		t.Errorf("runtime = %+v, want materialised agent", rt)
	}
}

func TestAgent_Run_PullFailureSetsFailurePull(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	a := newAgent(lifecycleDeps(t, cli))

	cli.EXPECT().ImageInspect(mock.Anything, DefaultBaseImage).
		Return(mobyclient.ImageInspectResult{}, errors.New("no such image")).Once()
	cli.EXPECT().ImagePull(mock.Anything, DefaultBaseImage, mock.Anything).
		Return(nil, errors.New("registry unreachable")).Once()

	if err := a.Run(context.Background()); err == nil {
		t.Fatal("expected pull failure")
	}

	if a.Status() != executor.StatusFailed || a.FailureReason() != executor.FailurePull {
		t.Errorf("status=%v reason=%v, want failed/pull", a.Status(), a.FailureReason())
	}
}

func TestAgent_Run_CreateFailureSetsFailureInit(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	a := newAgent(lifecycleDeps(t, cli))

	for _, ref := range []string{
		DefaultBaseImage,
		"ghcr.io/openotters/runtime:latest",
		"ghcr.io/openotters/tools/jq:latest",
	} {
		cli.EXPECT().ImageInspect(mock.Anything, ref).
			Return(mobyclient.ImageInspectResult{}, nil).Once()
	}

	cli.EXPECT().ContainerRemove(mock.Anything, mock.Anything, mock.Anything).
		Return(mobyclient.ContainerRemoveResult{}, notFoundError{}).Once()
	cli.EXPECT().ContainerCreate(mock.Anything, mock.Anything).
		Return(mobyclient.ContainerCreateResult{}, errors.New("daemon on fire")).Once()

	if err := a.Run(context.Background()); err == nil {
		t.Fatal("expected create failure")
	}

	if a.Status() != executor.StatusFailed || a.FailureReason() != executor.FailureInit {
		t.Errorf("status=%v reason=%v, want failed/init", a.Status(), a.FailureReason())
	}
}

func TestAgent_Run_PrepareFailureOnMissingArtifact(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)

	deps := lifecycleDeps(t, cli)
	deps.ref = spec.ParseReference("ghost/agent:none") // not in the store

	a := newAgent(deps)

	if err := a.Run(context.Background()); err == nil {
		t.Fatal("expected prepare failure for missing artifact")
	}

	if a.Status() != executor.StatusFailed {
		t.Errorf("status = %v, want failed", a.Status())
	}
}

func TestAgent_SetAgentToken(t *testing.T) {
	t.Parallel()

	cli := mockdocker.NewMockClient(t)
	a := newAgent(lifecycleDeps(t, cli))

	a.SetAgentToken("new-jwt")

	if a.deps.agentToken != "new-jwt" {
		t.Errorf("agentToken = %q, want new-jwt", a.deps.agentToken)
	}
}

// notFoundError mimics the daemon's 404 shape recognised by isNotFoundErr.
type notFoundError struct{}

func (notFoundError) Error() string  { return "No such container" }
func (notFoundError) NotFound() bool { return true }
