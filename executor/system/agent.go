// Package system implements executor.Provider using local OS processes and a
// chrooted billy filesystem per agent.
package system

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-billy/v6/util"
	"github.com/google/uuid"

	"github.com/openotters/agentfile/executor"
)

// Agent implements executor.Agent using a local OS process.
//
// mu guards the mutable run state (rt, addr, the daemon-callback pair, and the
// running/ran/cancel handshake). It is never held across slow I/O:
// materialisation runs unlocked, and only the resulting descriptor is
// published under the lock.
type Agent struct {
	id     uuid.UUID
	fs     billy.Filesystem
	ws     workspace
	proc   process
	status *executor.StatusTracker

	mu          sync.Mutex
	rt          *executor.Runtime
	addr        string
	daemonURL   string
	agentToken  string
	initialized bool
	running     bool
	ran         chan struct{}
	cancel      context.CancelFunc

	logCloser io.Closer
}

// NewAgent creates a system agent.
func NewAgent(id uuid.UUID, fs billy.Filesystem, opts ...AgentOption) *Agent {
	a := &Agent{
		id:     id,
		fs:     fs,
		proc:   process{spawner: defaultSpawner{}, stdout: os.Stdout, stderr: os.Stderr},
		status: executor.NewStatusTracker(),
	}

	// Real host filesystem by default so callers using WithAgentLocalRuntime
	// outside a Provider don't hit a nil hostFS; tests override with memfs.
	a.ws.hostFS = osfs.New("/")

	for _, opt := range opts {
		opt(a)
	}

	return a
}

// UUID returns the agent's stable identifier.
func (a *Agent) UUID() uuid.UUID { return a.id }

// Runtime returns the resolved runtime descriptor, or nil before Prepare.
func (a *Agent) Runtime() *executor.Runtime {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.rt
}

// Addr returns the loopback host:port the runtime binds.
func (a *Agent) Addr() string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.addr
}

// Status returns the current lifecycle state.
func (a *Agent) Status() executor.Status { return a.status.Get() }

// FailureReason returns the cause when Status is StatusFailed.
func (a *Agent) FailureReason() executor.FailureReason { return a.status.Failure() }

// StatusTracker exposes the tracker for the daemon supervisor's Ready/Working
// transitions and for tests.
func (a *Agent) StatusTracker() *executor.StatusTracker { return a.status }

// SubscribeStatus returns a channel of status transitions and a cancel function.
func (a *Agent) SubscribeStatus() (<-chan executor.Status, func()) {
	return a.status.Subscribe()
}

// SetAgentToken swaps the JWT injected into the spawn env. The running process
// keeps its token until restart; the next Run reads the new value.
func (a *Agent) SetAgentToken(token string) {
	a.mu.Lock()
	a.agentToken = token
	a.mu.Unlock()
}

// ReapplyMounts re-runs the mount symlink and MOUNTS.md steps against the
// existing filesystem, so mounts survive a daemon restart without a rebuild.
func (a *Agent) ReapplyMounts() error {
	if len(a.ws.mounts) == 0 {
		return nil
	}

	return a.ws.applyMounts(a.fs)
}

// Prepare materialises the workspace, mapping the failure to a FailureReason —
// unless ctx was cancelled (a deliberate Stop), which endRun settles to Stopped.
func (a *Agent) Prepare(ctx context.Context) error {
	if err := a.initialize(ctx); err != nil {
		if ctx.Err() == nil {
			a.status.SetFailure(failureReasonFor(err))
		}

		return err
	}

	return nil
}

// Run materialises the workspace (if needed) and blocks on the runtime process
// until it exits or ctx is cancelled. Only one Run may be in flight at a time.
//
// Lifecycle: Pulling → Starting → (supervisor sets Ready) → Stopped on exit.
// The cancel/ran handshake is installed before Prepare, so a Stop during the
// pull window cancels the run instead of silently no-op'ing.
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

	a.status.Set(executor.StatusStarting)

	return a.proc.serve(ctx, a.command())
}

// beginRun installs the run's cancellable context and ran channel, rejecting a
// concurrent Run.
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

// endRun clears the run state, closes ran, and settles the status to Stopped
// unless a terminal failure was already recorded.
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

// command builds the runtime Cmd from live state, so a token rotated between
// runs reaches the next spawn.
func (a *Agent) command() Cmd {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.proc.command(a.rt, a.fs.Root(), a.daemonURL, a.agentToken)
}

// Start re-runs a stopped or failed agent, re-resolving model credentials first
// so a providers.yaml edit takes effect on the next subprocess.
func (a *Agent) Start(ctx context.Context) error {
	switch a.Status() {
	case executor.StatusPulling, executor.StatusStarting,
		executor.StatusReady, executor.StatusWorking:
		return fmt.Errorf("agent already running")
	case executor.StatusRemoving, executor.StatusRemoved:
		return fmt.Errorf("agent removed")
	case executor.StatusStopped, executor.StatusFailed:
	}

	if err := a.reresolveCredentials(); err != nil {
		a.status.SetFailure(executor.FailureModel)

		return err
	}

	return a.Run(ctx)
}

// reresolveCredentials refreshes rt.APIBase/APIKey from the model resolver.
// No-op before Prepare, without a resolver, or for a provider-less model. The
// resolver runs without the lock held; only the field writes are guarded.
func (a *Agent) reresolveCredentials() error {
	a.mu.Lock()
	rt := a.rt
	resolver := a.ws.modelResolver
	a.mu.Unlock()

	if rt == nil || resolver == nil || rt.Model == "" {
		return nil
	}

	apiBase, apiKey, err := resolver(rt.Model)
	if err != nil {
		return errors.Join(ErrModel, fmt.Errorf("re-resolving model %q: %w", rt.Model, err))
	}

	a.mu.Lock()
	a.rt.APIBase = apiBase
	a.rt.APIKey = apiKey
	a.mu.Unlock()

	return nil
}

// Stop cancels the run and blocks until Run returns or ctx is cancelled. It is
// effective in every phase — pull, materialise, or serve — because it cancels
// the run context installed by beginRun. A no-op if the agent is not running.
func (a *Agent) Stop(ctx context.Context) error {
	a.mu.Lock()
	cancel := a.cancel
	ran := a.ran
	a.mu.Unlock()

	if cancel != nil {
		cancel()
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

// Remove stops the agent (if running) and deletes its workspace directory.
func (a *Agent) Remove(ctx context.Context) error {
	if err := a.Stop(ctx); err != nil {
		return err
	}

	a.status.Set(executor.StatusRemoving)

	if a.logCloser != nil {
		_ = a.logCloser.Close()
	}

	if a.fs != nil {
		if err := util.RemoveAll(a.fs, "."); err != nil {
			return fmt.Errorf("removing workspace: %w", err)
		}
	}

	a.status.Set(executor.StatusRemoved)

	return nil
}

// initialize materialises the workspace once and publishes the runtime
// descriptor. Failures are not cached, so a fixed providers.yaml unsticks a
// model_error agent on the next Start. The slow materialise runs unlocked.
func (a *Agent) initialize(ctx context.Context) error {
	a.mu.Lock()
	done := a.initialized
	addr := a.addr
	a.mu.Unlock()

	if done {
		return nil
	}

	rt, err := a.ws.materialize(ctx, a.fs, a.id, addr)
	if err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.initialized {
		a.rt = rt
		a.initialized = true
	}

	return nil
}

// markInitialized binds an already-materialised runtime onto an Agent, so
// Provider.Load can recover agents from disk without re-materialising.
func (a *Agent) markInitialized(rt *executor.Runtime) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.initialized {
		a.rt = rt
		a.initialized = true
	}
}

// failureReasonFor maps a materialise error to its FailureReason.
func failureReasonFor(err error) executor.FailureReason {
	switch {
	case errors.Is(err, ErrPull):
		return executor.FailurePull
	case errors.Is(err, ErrModel):
		return executor.FailureModel
	case errors.Is(err, ErrConfig):
		return executor.FailureConfig
	default:
		return executor.FailureInit
	}
}
