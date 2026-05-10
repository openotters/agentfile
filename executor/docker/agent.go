package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/go-git/go-billy/v6"
	"github.com/google/uuid"
	mobyclient "github.com/moby/moby/client"
	"google.golang.org/grpc"
	"oras.land/oras-go/v2"

	"github.com/openotters/agentfile/executor"
	"github.com/openotters/agentfile/executor/system"
	"github.com/openotters/agentfile/model"
	agentoci "github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
)

// agentDeps groups the dependencies the docker Agent needs at
// construction. Pulled out into its own struct because there are
// enough of them that the constructor would otherwise be a wall
// of positional args.
type agentDeps struct {
	id           uuid.UUID
	client       Client
	baseImage    string
	ref          spec.Reference
	overrides    []spec.Override
	fs           billy.Filesystem // per-agent chrooted host FS (bind-mount source)
	hostFS       billy.Filesystem // non-chrooted host FS
	store        oras.ReadOnlyTarget
	puller       agentoci.Puller
	modelResolve model.Resolver
	mounts       []executor.Mount
	hostGRPCPort string
	logDir       string
}

// Agent is the Docker-backed implementation of executor.Agent.
type Agent struct {
	deps   agentDeps
	status *executor.StatusTracker

	mu          sync.Mutex
	containerID string
	rt          *executor.Runtime
	initialized bool
	ran         chan struct{}

	// gRPC connection to the runtime inside the container,
	// reused across Prompt / PromptStream / PromptObject calls.
	// Closed by closeClient on Stop/Remove. Defined in prompt.go
	// to keep the gRPC dependency localized.
	clientMu   sync.Mutex
	clientConn *grpc.ClientConn
}

func newAgent(deps agentDeps) *Agent {
	return &Agent{
		deps:   deps,
		status: executor.NewStatusTracker(),
	}
}

// UUID is the agent's stable identifier across Stop/Start cycles.
func (a *Agent) UUID() uuid.UUID { return a.deps.id }

// Runtime returns the resolved runtime descriptor populated at
// Prepare/Run time.
func (a *Agent) Runtime() *executor.Runtime {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.rt
}

// Status returns the current lifecycle state.
func (a *Agent) Status() executor.Status { return a.status.Get() }

// SubscribeStatus returns a channel of status transitions and a
// cancel function.
func (a *Agent) SubscribeStatus() (<-chan executor.Status, func()) {
	return a.status.Subscribe()
}

// Prepare materialises the agent's content workspace. Idempotent.
func (a *Agent) Prepare(ctx context.Context) error {
	a.mu.Lock()
	already := a.initialized
	a.mu.Unlock()

	if already {
		return nil
	}

	rt, err := system.MaterializeContent(
		ctx, a.deps.fs, a.deps.id,
		"127.0.0.1:"+a.deps.hostGRPCPort,
		system.MaterializeOptions{
			Store:         a.deps.store,
			Ref:           a.deps.ref,
			Overrides:     a.deps.overrides,
			OCIPuller:     a.deps.puller,
			ModelResolver: a.deps.modelResolve,
			ImageRef:      a.deps.ref.String(),
			Mounts:        a.deps.mounts,
			HostFS:        a.deps.hostFS,
			// Tool binaries live at the image-mount path inside
			// the container — the runtime never sees the bind-
			// mounted /workspace's usr/bin/, which is empty for
			// the docker executor.
			ToolBinaryPath: func(name string) string {
				return inContainerBinsRoot + "/" + name + "/" + name
			},
			// WORKSPACE.md is rendered from the agent's container
			// view: Root = /agent (FHS bind target), WorkspaceDir
			// = /workspace (the scratch bind), runtime at
			// /opt/runtime/runtime, one BinDir per declared BIN at
			// /opt/bins/<name>. Decoupling Root from WorkspaceDir
			// is what gives the agent a clean top-level CWD
			// instead of the doubled-up /workspace/workspace.
			View: executor.WorkspaceView{
				Root:         inContainerAgentRoot,
				WorkspaceDir: inContainerWorkspace,
				RuntimeBin:   inContainerRuntimeDir + "/runtime",
				Isolated:     true,
			},
			ViewBinDirsForTools: func(names []string) []string {
				dirs := make([]string, 0, len(names))
				for _, n := range names {
					dirs = append(dirs, inContainerBinsRoot+"/"+n)
				}

				return dirs
			},
		},
	)
	if err != nil {
		switch {
		case errors.Is(err, system.ErrPull):
			a.status.Set(executor.StatusPullError)
		case errors.Is(err, system.ErrModel):
			a.status.Set(executor.StatusModelError)
		default:
			a.status.Set(executor.StatusInitError)
		}

		return err
	}

	a.mu.Lock()
	a.rt = rt
	a.initialized = true
	a.mu.Unlock()

	return nil
}

// Run materialises (if needed), starts the runtime container, and
// blocks until the container exits or ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.Prepare(ctx); err != nil {
		return err
	}

	a.status.Set(executor.StatusRunning)

	ran := make(chan struct{})
	a.setRan(ran)

	defer func() {
		close(ran)
		a.status.Set(executor.StatusStopped)
	}()

	if err := a.ensureImagesPulled(ctx); err != nil {
		a.status.Set(executor.StatusInitError)
		return err
	}

	if err := a.create(ctx); err != nil {
		a.status.Set(executor.StatusInitError)
		return err
	}

	if err := a.start(ctx); err != nil {
		a.status.Set(executor.StatusInitError)
		return err
	}

	return a.wait(ctx)
}

// Start re-runs a stopped agent.
func (a *Agent) Start(ctx context.Context) error {
	switch a.Status() {
	case executor.StatusRunning:
		return fmt.Errorf("agent already running")
	case executor.StatusRemoving, executor.StatusRemoved:
		return fmt.Errorf("agent removed")
	case executor.StatusCreated, executor.StatusStopped,
		executor.StatusInitError, executor.StatusPullError, executor.StatusModelError:
	}

	return a.Run(ctx)
}

// Stop signals the running container to exit and waits for the
// Run goroutine to return.
func (a *Agent) Stop(ctx context.Context) error {
	id := a.idLocked()
	if id == "" {
		return nil
	}

	timeout := 10
	if _, err := a.deps.client.ContainerStop(ctx, id, mobyclient.ContainerStopOptions{Timeout: &timeout}); err != nil {
		return fmt.Errorf("docker: stop %s: %w", id, err)
	}

	ran := a.getRan()
	if ran == nil {
		return nil
	}

	select {
	case <-ran:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Remove tears down the container and the agent's on-disk content.
func (a *Agent) Remove(ctx context.Context) error {
	a.status.Set(executor.StatusRemoving)
	defer a.status.Set(executor.StatusRemoved)

	id := a.idLocked()
	if id != "" {
		if _, err := a.deps.client.ContainerRemove(ctx, id, mobyclient.ContainerRemoveOptions{Force: true}); err != nil {
			return fmt.Errorf("docker: remove container %s: %w", id, err)
		}

		a.mu.Lock()
		a.containerID = ""
		a.mu.Unlock()
	}

	return nil
}

// ensureImagesPulled pulls the base image, runtime image, and each
// BIN image into Docker's local cache so the container's image
// mounts resolve. Skips refs Docker already has locally — pulling
// overwrites local-only builds (e.g. `docker buildx build --load`),
// which breaks development flows where a stale upstream tag exists
// alongside a fresh local one.
func (a *Agent) ensureImagesPulled(ctx context.Context) error {
	rt := a.Runtime()
	if rt == nil {
		return fmt.Errorf("docker: no runtime resolved (Prepare not called)")
	}

	refs := []string{a.deps.baseImage}

	runtimeRef := a.runtimeRef(rt)
	if runtimeRef != "" {
		refs = append(refs, runtimeRef)
	}

	for _, t := range rt.Tools {
		if t.Ref != "" {
			refs = append(refs, t.Ref)
		}
	}

	for _, ref := range refs {
		if a.imageCached(ctx, ref) {
			continue
		}
		if err := a.pullImage(ctx, ref); err != nil {
			return fmt.Errorf("docker: pull %s: %w", ref, err)
		}
	}

	return nil
}

func (a *Agent) imageCached(ctx context.Context, ref string) bool {
	_, err := a.deps.client.ImageInspect(ctx, ref)

	return err == nil
}

func (a *Agent) pullImage(ctx context.Context, ref string) error {
	rc, err := a.deps.client.ImagePull(ctx, ref, mobyclient.ImagePullOptions{
		// Resolve credentials from ~/.docker/config.json so private
		// registries (and ghcr.io's broken anonymous-token endpoint)
		// keep working. Empty when no entry matches — the daemon then
		// falls back to its anonymous-pull path.
		RegistryAuth: resolveRegistryAuth(ref),
	})
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()

	if _, copyErr := io.Copy(io.Discard, rc); copyErr != nil {
		return copyErr
	}

	return nil
}

func (a *Agent) runtimeRef(rt *executor.Runtime) string {
	if rt.Provenance != nil && rt.Provenance.RuntimeRef != "" {
		return rt.Provenance.RuntimeRef
	}
	if rt.Source != nil {
		return rt.Source.Agent.Runtime
	}
	return ""
}

// removeOrphanContainer force-removes any existing container with
// the given name. Used by create() to clear a stopped leftover
// from a previous Run before issuing ContainerCreate (the Docker
// daemon rejects duplicate names with 409). Not-found errors are
// swallowed — the absence is the desired state.
func (a *Agent) removeOrphanContainer(ctx context.Context, name string) error {
	if _, err := a.deps.client.ContainerRemove(ctx, name, mobyclient.ContainerRemoveOptions{
		Force: true,
	}); err != nil {
		if isNotFoundErr(err) {
			return nil
		}

		return fmt.Errorf("docker: remove orphan %s: %w", name, err)
	}

	return nil
}

// create assembles the ContainerCreateOptions from the resolved
// runtime + Provider config and posts to the daemon.
func (a *Agent) create(ctx context.Context) error {
	rt := a.Runtime()
	if rt == nil {
		return fmt.Errorf("docker: no runtime")
	}

	bins := make(map[string]string, len(rt.Tools))
	for _, t := range rt.Tools {
		bins[t.Name] = t.Ref
	}

	provider, _ := splitProviderPrefix(rt.Model)

	var userEnvs []*spec.Env
	if rt.Source != nil && rt.Source.Agent != nil {
		userEnvs = rt.Source.Agent.Envs
	}

	// Per-run user mounts come through spec.WithMounts populating
	// rt.Source.Agent.RuntimeMounts; the legacy provider-level
	// docker.WithMounts (deps.mounts) is the fallback for callers
	// that haven't migrated yet.
	userMounts := a.deps.mounts
	if rt.Source != nil && rt.Source.Agent != nil && len(rt.Source.Agent.RuntimeMounts) > 0 {
		userMounts = make([]executor.Mount, 0, len(rt.Source.Agent.RuntimeMounts))
		for _, m := range rt.Source.Agent.RuntimeMounts {
			if m == nil {
				continue
			}
			userMounts = append(userMounts, executor.Mount{
				Host:        m.Host,
				Target:      m.Target,
				Description: m.Description,
				ReadOnly:    m.ReadOnly,
			})
		}
	}

	cs := containerSpec{
		Name:          "otters-" + a.deps.id.String(),
		BaseImage:     a.deps.baseImage,
		AgentRoot:     a.deps.fs.Root(),
		RuntimeImage:  a.runtimeRef(rt),
		BINImages:     bins,
		UserMounts:    userMounts,
		NetworkAccess: true,
		APIBase:       rt.APIBase,
		APIKey:        rt.APIKey,
		Provider:      provider,
		Model:         rt.Model,
		HostGRPCPort:  a.deps.hostGRPCPort,
		UserEnvs:      userEnvs,
	}

	// A previous Run may have left a stopped container with the
	// same name (`otters-<agent-id>`); ContainerCreate would 409 on
	// the name collision and the failure can flow through Run as a
	// silent "container created but not started" — Status stays at
	// Stopped, no log surfaces. Remove the orphan first so the
	// fresh Create + Start path runs cleanly.
	if removeErr := a.removeOrphanContainer(ctx, cs.Name); removeErr != nil {
		return removeErr
	}

	resp, err := a.deps.client.ContainerCreate(ctx, mobyclient.ContainerCreateOptions{
		Config:     cs.buildConfig(),
		HostConfig: cs.buildHostConfig(),
		Name:       cs.Name,
	})
	if err != nil {
		return fmt.Errorf("docker: create: %w", err)
	}

	a.mu.Lock()
	a.containerID = resp.ID
	a.mu.Unlock()

	return nil
}

func (a *Agent) start(ctx context.Context) error {
	id := a.idLocked()
	if id == "" {
		return fmt.Errorf("docker: no container created")
	}

	if _, err := a.deps.client.ContainerStart(ctx, id, mobyclient.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("docker: start %s: %w", id, err)
	}

	return nil
}

// wait blocks on container logs until the container exits (logs EOF)
// or ctx is cancelled. Drains logs into io.Discard for now; daemon-
// side log capture / streaming is a follow-up.
func (a *Agent) wait(ctx context.Context) error {
	id := a.idLocked()
	if id == "" {
		return nil
	}

	rc, err := a.deps.client.ContainerLogs(ctx, id, mobyclient.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
	})
	if err != nil {
		return fmt.Errorf("docker: stream logs: %w", err)
	}
	defer func() { _ = rc.Close() }()

	if _, copyErr := io.Copy(io.Discard, rc); copyErr != nil && !errors.Is(copyErr, context.Canceled) {
		return fmt.Errorf("docker: drain logs: %w", copyErr)
	}

	return nil
}

func (a *Agent) idLocked() string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.containerID
}

func (a *Agent) setRan(ch chan struct{}) {
	a.mu.Lock()
	a.ran = ch
	a.mu.Unlock()
}

func (a *Agent) getRan() chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.ran
}

// splitProviderPrefix extracts the provider prefix from a fully
// qualified model name (e.g. "anthropic/claude-..." → "anthropic").
func splitProviderPrefix(modelName string) (string, string) {
	if idx := strings.Index(modelName, "/"); idx > 0 {
		return modelName[:idx], modelName[idx+1:]
	}
	return "", modelName
}
