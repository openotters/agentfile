package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/util"
	"github.com/google/uuid"
	mobyclient "github.com/moby/moby/client"
	"oras.land/oras-go/v2"

	"github.com/openotters/agentfile/executor"
	"github.com/openotters/agentfile/executor/system"
	"github.com/openotters/agentfile/model"
	agentoci "github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
)

// stopGraceSeconds is how long ContainerStop waits for SIGTERM before the
// daemon force-kills the container.
const stopGraceSeconds = 10

// agentDeps groups the docker Agent's construction dependencies.
type agentDeps struct {
	id           uuid.UUID
	client       Client
	baseImage    string
	ref          spec.Reference
	overrides    []spec.Override
	fs           billy.Filesystem // per-agent chroot (bind-mount source)
	hostFS       billy.Filesystem // non-chrooted host FS
	store        oras.ReadOnlyTarget
	puller       agentoci.Puller
	usageFetcher agentoci.UsageFetcher
	modelResolve model.Resolver
	mounts       []executor.Mount
	hostGRPCPort string
	logDir       string
	daemonURL    string
	agentToken   string
	capabilities []executor.Capability
}

// Agent runs the runtime inside a Docker container.
//
// mu guards the mutable run state (containerID, rt, the daemon token, and the
// running/ran/cancel handshake) so a token rotation or a Stop cannot race a
// container create.
type Agent struct {
	deps   agentDeps
	status *executor.StatusTracker

	mu          sync.Mutex
	containerID string
	rt          *executor.Runtime
	initialized bool
	running     bool
	ran         chan struct{}
	cancel      context.CancelFunc
}

func newAgent(deps agentDeps) *Agent {
	return &Agent{deps: deps, status: executor.NewStatusTracker()}
}

// UUID returns the agent's stable identifier.
func (a *Agent) UUID() uuid.UUID { return a.deps.id }

// Runtime returns the resolved runtime descriptor, or nil before Prepare.
func (a *Agent) Runtime() *executor.Runtime {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.rt
}

// Status returns the current lifecycle state.
func (a *Agent) Status() executor.Status { return a.status.Get() }

// FailureReason returns the cause when Status is StatusFailed.
func (a *Agent) FailureReason() executor.FailureReason { return a.status.Failure() }

// StatusTracker exposes the tracker for the daemon supervisor and tests.
func (a *Agent) StatusTracker() *executor.StatusTracker { return a.status }

// SubscribeStatus returns a channel of status transitions and a cancel function.
func (a *Agent) SubscribeStatus() (<-chan executor.Status, func()) {
	return a.status.Subscribe()
}

// SetAgentToken swaps the JWT injected into the next container's env. The
// running container keeps its token until restart.
func (a *Agent) SetAgentToken(token string) {
	a.mu.Lock()
	a.deps.agentToken = token
	a.mu.Unlock()
}

// Prepare materialises the agent's content workspace. Idempotent.
func (a *Agent) Prepare(ctx context.Context) error {
	a.mu.Lock()
	done := a.initialized
	a.mu.Unlock()

	if done {
		return nil
	}

	rt, err := system.MaterializeContent(ctx, a.deps.fs, a.deps.id, a.Addr(), a.materializeOptions())
	if err != nil {
		a.failUnless(ctx, materializeFailureReason(err))

		return err
	}

	a.mu.Lock()
	a.rt = rt
	a.initialized = true
	a.mu.Unlock()

	return nil
}

// Addr returns the host loopback address the runtime's server is published on.
// The orchestrator dials it to reach the runtime; the wire protocol is the
// orchestrator's concern. Empty until a port is reserved.
func (a *Agent) Addr() string {
	if a.deps.hostGRPCPort == "" {
		return ""
	}

	return "127.0.0.1:" + a.deps.hostGRPCPort
}

// materializeOptions maps the docker container layout onto the shared
// materialiser: the runtime and BINs arrive as image mounts under /opt, so the
// tool paths and workspace view differ from the system executor's FHS layout.
func (a *Agent) materializeOptions() system.MaterializeOptions {
	return system.MaterializeOptions{
		Store:          a.deps.store,
		Ref:            a.deps.ref,
		Overrides:      a.deps.overrides,
		OCIPuller:      a.deps.puller,
		UsageFetcher:   a.deps.usageFetcher,
		ModelResolver:  a.deps.modelResolve,
		ImageRef:       a.deps.ref.String(),
		Mounts:         a.deps.mounts,
		HostFS:         a.deps.hostFS,
		Capabilities:   a.deps.capabilities,
		ToolBinaryPath: func(name string) string { return inContainerBinsRoot + "/" + name },
		SymlinkBinAt:   func(name string) (string, bool) { return inContainerBinImages + "/" + name + "/" + name, true },
		View: executor.WorkspaceView{
			Root:         inContainerAgentRoot,
			WorkspaceDir: inContainerWorkspace,
			RuntimeBin:   inContainerRuntimeDir + "/runtime",
			Isolated:     true,
		},
		ViewBinDirsForTools: func([]string) []string { return []string{inContainerBinsRoot} },
	}
}

// Run materialises (if needed), pulls images, and runs the container until it
// exits or ctx is cancelled. Only one Run may be in flight at a time; the
// cancel/ran handshake is installed before Prepare so a Stop during the pull
// window cancels the run.
func (a *Agent) Run(ctx context.Context) error {
	ctx, ran, err := a.beginRun(ctx)
	if err != nil {
		return err
	}
	defer a.endRun(ran)

	a.status.Set(executor.StatusPulling)

	if prepErr := a.Prepare(ctx); prepErr != nil {
		return prepErr
	}

	if pullErr := a.ensureImagesPulled(ctx); pullErr != nil {
		a.failUnless(ctx, executor.FailurePull)

		return pullErr
	}

	a.status.Set(executor.StatusStarting)

	if createErr := a.create(ctx); createErr != nil {
		a.failUnless(ctx, executor.FailureInit)

		return createErr
	}

	if startErr := a.start(ctx); startErr != nil {
		a.failUnless(ctx, executor.FailureInit)

		return startErr
	}

	return a.wait(ctx)
}

// failUnless records a failure reason unless ctx was cancelled — a cancellation
// is a deliberate Stop, which endRun settles to Stopped.
func (a *Agent) failUnless(ctx context.Context, reason executor.FailureReason) {
	if ctx.Err() == nil {
		a.status.SetFailure(reason)
	}
}

func (a *Agent) beginRun(ctx context.Context) (context.Context, chan struct{}, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.running {
		return nil, nil, fmt.Errorf("agent already running")
	}

	runCtx, cancel := context.WithCancel(ctx)
	ran := make(chan struct{})
	a.running = true
	a.cancel = cancel
	a.ran = ran

	return runCtx, ran, nil
}

func (a *Agent) endRun(ran chan struct{}) {
	a.mu.Lock()
	a.running = false
	a.cancel = nil
	a.ran = nil
	a.mu.Unlock()

	// Settle the status before releasing waiters, so a Stop/Remove blocked on
	// ran cannot race the settle and flip a removed agent back to stopped.
	a.status.SetUnless(executor.StatusStopped, executor.StatusFailed, executor.StatusRemoving, executor.StatusRemoved)
	close(ran)
}

// Start re-runs a stopped or failed agent.
func (a *Agent) Start(ctx context.Context) error {
	switch a.Status() {
	case executor.StatusPulling, executor.StatusStarting,
		executor.StatusReady, executor.StatusWorking:
		return fmt.Errorf("agent already running")
	case executor.StatusRemoving, executor.StatusRemoved:
		return fmt.Errorf("agent removed")
	case executor.StatusStopped, executor.StatusFailed:
	}

	return a.Run(ctx)
}

// Stop cancels the run, stops the container, and waits for Run to return.
func (a *Agent) Stop(ctx context.Context) error {
	a.mu.Lock()
	cancel := a.cancel
	ran := a.ran
	id := a.containerID
	a.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	if id != "" {
		a.stopContainer(ctx, id)
	}

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

// stopContainer asks the daemon to stop id, tolerating an already-gone
// container.
func (a *Agent) stopContainer(ctx context.Context, id string) {
	timeout := stopGraceSeconds
	if _, err := a.deps.client.ContainerStop(ctx, id, mobyclient.ContainerStopOptions{Timeout: &timeout}); err != nil &&
		!isNotFoundErr(err) {
		// Best effort: a stop failure is surfaced by the caller's wait/remove.
		_ = err
	}
}

// Remove stops the agent, removes its container, and deletes its on-disk tree.
func (a *Agent) Remove(ctx context.Context) error {
	if err := a.Stop(ctx); err != nil {
		return err
	}

	a.status.Set(executor.StatusRemoving)

	if id := a.idLocked(); id != "" {
		if _, err := a.deps.client.ContainerRemove(ctx, id, mobyclient.ContainerRemoveOptions{Force: true}); err != nil &&
			!isNotFoundErr(err) {
			return fmt.Errorf("docker: remove container %s: %w", id, err)
		}

		a.mu.Lock()
		a.containerID = ""
		a.mu.Unlock()
	}

	if a.deps.fs != nil {
		if err := util.RemoveAll(a.deps.fs, "."); err != nil {
			return fmt.Errorf("docker: remove workspace: %w", err)
		}
	}

	a.status.Set(executor.StatusRemoved)

	return nil
}

// ensureImagesPulled pulls the base, runtime, and BIN images not already in
// Docker's local cache so the container's image mounts resolve.
func (a *Agent) ensureImagesPulled(ctx context.Context) error {
	rt := a.Runtime()
	if rt == nil {
		return fmt.Errorf("docker: no runtime resolved (Prepare not called)")
	}

	refs := []string{a.deps.baseImage}
	if runtimeRef := a.runtimeRef(rt); runtimeRef != "" {
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
		RegistryAuth: resolveRegistryAuth(ref),
	})
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()

	_, err = io.Copy(io.Discard, rc)

	return err
}

func (a *Agent) runtimeRef(rt *executor.Runtime) string {
	if rt.Runtime != nil {
		return rt.Runtime.Ref
	}

	if rt.Source != nil && rt.Source.Agent != nil {
		return rt.Source.Agent.Runtime
	}

	return ""
}

// create builds the container spec from the resolved runtime and posts it,
// clearing any orphan of the same name from a previous run first.
func (a *Agent) create(ctx context.Context) error {
	a.mu.Lock()
	rt := a.rt
	daemonURL := a.deps.daemonURL
	agentToken := a.deps.agentToken
	a.mu.Unlock()

	if rt == nil {
		return fmt.Errorf("docker: no runtime")
	}

	provider, _ := splitProviderPrefix(rt.Model)

	cs := containerSpec{
		Name:         "otters-" + a.deps.id.String(),
		BaseImage:    a.deps.baseImage,
		AgentRoot:    a.deps.fs.Root(),
		RuntimeImage: a.runtimeRef(rt),
		BINImages:    toolRefs(rt.Tools),
		UserMounts:   effectiveMounts(a.deps.mounts, rt.Mounts),
		APIBase:      rt.APIBase,
		APIKey:       rt.APIKey,
		Provider:     provider,
		Model:        rt.Model,
		Exec:         rt.Exec,
		HostGRPCPort: a.deps.hostGRPCPort,
		DaemonURL:    daemonURL,
		AgentToken:   agentToken,
		UserEnvs:     rt.Envs,
		Configs:      rt.Configs,
	}

	if err := a.removeOrphanContainer(ctx, cs.Name); err != nil {
		return err
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

// removeOrphanContainer clears a stopped leftover of the same name so
// ContainerCreate does not 409.
func (a *Agent) removeOrphanContainer(ctx context.Context, name string) error {
	if _, err := a.deps.client.ContainerRemove(ctx, name, mobyclient.ContainerRemoveOptions{Force: true}); err != nil &&
		!isNotFoundErr(err) {
		return fmt.Errorf("docker: remove orphan %s: %w", name, err)
	}

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

// wait blocks until the container exits or ctx is cancelled. On cancellation it
// stops the container so it does not orphan and returns nil (a deliberate
// stop). On a natural exit it returns a non-nil error if the exit code was
// non-zero, so the supervisor can tell a crash from a clean stop.
func (a *Agent) wait(ctx context.Context) error {
	id := a.idLocked()
	if id == "" {
		return nil
	}

	rc, err := a.deps.client.ContainerLogs(ctx, id, mobyclient.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: true,
	})
	if err != nil {
		return fmt.Errorf("docker: stream logs: %w", err)
	}

	_, _ = io.Copy(io.Discard, rc)
	_ = rc.Close()

	if ctx.Err() != nil {
		a.stopContainer(context.WithoutCancel(ctx), id)

		return nil //nolint:nilerr // ctx cancellation is a deliberate stop, not a runtime error
	}

	if code := a.exitCode(id); code != 0 {
		return fmt.Errorf("docker: runtime exited with code %d", code)
	}

	return nil
}

// exitCode returns the container's exit code, or 0 if it cannot be read.
func (a *Agent) exitCode(id string) int {
	insp, err := a.deps.client.ContainerInspect(context.Background(), id, mobyclient.ContainerInspectOptions{})
	if err != nil || insp.Container.State == nil {
		return 0
	}

	return insp.Container.State.ExitCode
}

func (a *Agent) idLocked() string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.containerID
}

// toolRefs maps resolved tools to their name→image-ref pairs.
func toolRefs(tools []executor.ResolvedTool) map[string]string {
	refs := make(map[string]string, len(tools))
	for _, t := range tools {
		refs[t.Name] = t.Ref
	}

	return refs
}

// effectiveMounts prefers the daemon-hydrated rt.Mounts and falls back to the
// provider-level mounts for callers that have not migrated.
func effectiveMounts(fallback []executor.Mount, rtMounts []*spec.Mount) []executor.Mount {
	if len(rtMounts) == 0 {
		return fallback
	}

	out := make([]executor.Mount, 0, len(rtMounts))
	for _, m := range rtMounts {
		if m == nil {
			continue
		}

		out = append(out, executor.Mount{
			Host:        m.Host,
			Target:      m.Target,
			Description: m.Description,
			ReadOnly:    m.ReadOnly,
		})
	}

	return out
}

// materializeFailureReason maps a materialise error to its FailureReason.
func materializeFailureReason(err error) executor.FailureReason {
	switch {
	case errors.Is(err, system.ErrPull):
		return executor.FailurePull
	case errors.Is(err, system.ErrModel):
		return executor.FailureModel
	case errors.Is(err, system.ErrConfig):
		return executor.FailureConfig
	default:
		return executor.FailureInit
	}
}

// splitProviderPrefix splits "provider/model" at the first slash.
func splitProviderPrefix(modelName string) (string, string) {
	if idx := strings.Index(modelName, "/"); idx > 0 {
		return modelName[:idx], modelName[idx+1:]
	}

	return "", modelName
}
