// Package system implements executor.Provider using local OS processes and a
// chrooted billy filesystem per agent.
package system

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/util"
	"github.com/google/uuid"
	"google.golang.org/grpc"

	"github.com/openotters/agentfile/executor"
)

// Agent implements executor.Agent using local OS processes.
type Agent struct {
	id     uuid.UUID
	fs     billy.Filesystem
	ws     workspace
	proc   process
	rt     *executor.Runtime
	addr   string
	cmdFn  cmdFunc
	dialer Dialer
	// daemonURL + agentToken are injected into the spawn env so the
	// runtime knows where to dial the openotters daemon and what
	// JWT to present. Both empty by default — agents whose author
	// doesn't want to expose the daemon path simply don't pass
	// these options at construction. daemonURL is the dial target
	// (e.g. unix:///tmp/otters.sock); injected as-is into
	// OTTERSD_URL.
	daemonURL  string
	agentToken string

	initMu      sync.Mutex
	initialized bool
	ran         chan struct{}

	clientMu   sync.Mutex
	clientConn *grpc.ClientConn

	status *executor.StatusTracker
}

// NewAgent creates a system agent.
func NewAgent(id uuid.UUID, fs billy.Filesystem, opts ...AgentOption) *Agent {
	a := &Agent{
		id: id,
		fs: fs,
		proc: process{
			spawner: defaultSpawner{},
			stdout:  os.Stdout,
			stderr:  os.Stderr,
		},
		dialer: defaultDialer{},
		status: executor.NewStatusTracker(),
	}

	for _, opt := range opts {
		opt(a)
	}

	return a
}

// UUID returns the agent's stable identifier, unchanged across
// Start/Stop/Restore cycles.
func (a *Agent) UUID() uuid.UUID { return a.id }

// Runtime returns the resolved runtime descriptor populated at
// Prepare/materialize time. Nil before Prepare has succeeded.
func (a *Agent) Runtime() *executor.Runtime { return a.rt }

// Addr returns the loopback host:port the runtime subprocess will
// bind (reserved by the Provider's LoopbackAllocator at Create).
// Empty on agents constructed without WithAddr and before Prepare.
func (a *Agent) Addr() string { return a.addr }

// Status returns the current lifecycle state. Safe for concurrent
// access with Start/Stop/Remove.
func (a *Agent) Status() executor.Status { return a.status.Get() }

// FailureReason returns the cause when Status() == StatusFailed.
// Returns FailureNone for non-failed states.
func (a *Agent) FailureReason() executor.FailureReason { return a.status.Failure() }

// StatusTracker exposes the underlying tracker. The daemon supervisor
// uses it for the Ready / Working / readiness-timeout / crashed
// transitions it owns; tests reach for it to assert specific
// sequences without coupling to the Set/SetFailure call sites.
func (a *Agent) StatusTracker() *executor.StatusTracker { return a.status }

// SubscribeStatus returns a channel of status transitions and a
// cancel function. Sends are non-blocking; slow subscribers may miss
// intermediate transitions — call Status() to resync. Always call
// cancel to avoid leaking the subscription.
func (a *Agent) SubscribeStatus() (<-chan executor.Status, func()) {
	return a.status.Subscribe()
}

// ReapplyMounts re-runs the chroot symlink step + MOUNTS.md context
// write against the agent's existing filesystem. Used by Daemon.Restore
// for agents loaded from disk (which skip materialize), so mounts
// survive a daemon restart without requiring a full rebuild.
func (a *Agent) ReapplyMounts() error {
	if len(a.ws.mounts) == 0 {
		return nil
	}

	return a.ws.applyMounts(a.fs)
}

// Prepare materializes the workspace synchronously. Idempotent and safe to
// call concurrently; repeat callers observe the same error.
//
// Status transitions on the way through:
//   - On entry: Pulling (set by Run; Prepare leaves it alone so a
//     standalone Prepare call still surfaces the pull window).
//   - On success: caller (Run) flips to Starting.
//   - On error: Failed with a FailureReason derived from the error
//     sentinel (FailurePull / FailureModel / FailureInit).
func (a *Agent) Prepare(ctx context.Context) error {
	if err := a.initialize(ctx); err != nil {
		switch {
		case errors.Is(err, ErrPull):
			a.status.SetFailure(executor.FailurePull)
		case errors.Is(err, ErrModel):
			a.status.SetFailure(executor.FailureModel)
		default:
			a.status.SetFailure(executor.FailureInit)
		}

		return err
	}

	return nil
}

// Run materializes the workspace (if needed), starts the serve process, and blocks.
//
// Lifecycle: Pulling (entry) → Starting (after Prepare returns) →
// (daemon supervisor flips to Ready once the readiness probe answers)
// → Stopped on subprocess exit (the daemon decides whether to retry
// or mark Failed+FailureCrashed).
func (a *Agent) Run(ctx context.Context) error {
	a.status.Set(executor.StatusPulling)

	if err := a.Prepare(ctx); err != nil {
		return err
	}

	a.status.Set(executor.StatusStarting)

	ran := make(chan struct{})
	a.setRan(ran)

	defer func() {
		close(ran)
		// Preserve Failed: anything inside proc.serve that set a
		// FailureReason should outlive this unconditional clean-up.
		if a.status.Get() != executor.StatusFailed {
			a.status.Set(executor.StatusStopped)
		}
	}()

	return a.proc.serve(ctx, a.cmdFn)
}

// Start re-runs a stopped (or failed) agent. Blocks until the
// subprocess exits or ctx is cancelled (same contract as Run).
// Returns an error if the agent is already pulling / starting /
// ready / working / removed. The loopback address from the original
// Create is reused.
//
// Before re-running, Start re-invokes the model resolver against the
// agent's resolved model, so providers.yaml edits made between Stop
// and Start (key rotation, api-base change) take effect on the next
// subprocess. Fresh agents (still in StatusPulling) bypass this branch
// — Run → Prepare → materialize will resolve on its own.
func (a *Agent) Start(ctx context.Context) error {
	switch a.Status() {
	case executor.StatusPulling, executor.StatusStarting,
		executor.StatusReady, executor.StatusWorking:
		return fmt.Errorf("agent already running")
	case executor.StatusRemoving, executor.StatusRemoved:
		return fmt.Errorf("agent removed")
	case executor.StatusStopped, executor.StatusFailed:
		// startable: fall through
	}

	if err := a.reresolveCredentials(); err != nil {
		a.status.SetFailure(executor.FailureModel)

		return err
	}

	return a.Run(ctx)
}

// reresolveCredentials re-runs the model resolver against the agent's
// already-resolved model and updates rt.APIBase / rt.APIKey in place.
// process.buildCmdArgs reads both fields on every closure invocation,
// so the next subprocess sees the fresh values without rebuilding cmdFn.
//
// No-op when there is no rt yet (StatusCreated agents take the
// Run → Prepare → materialize path), no resolver was wired, or the
// model has no provider prefix.
func (a *Agent) reresolveCredentials() error {
	if a.rt == nil || a.ws.modelResolver == nil || a.rt.Model == "" {
		return nil
	}

	apiBase, apiKey, err := a.ws.modelResolver(a.rt.Model)
	if err != nil {
		return errors.Join(ErrModel,
			fmt.Errorf("re-resolving model %q: %w", a.rt.Model, err))
	}

	a.rt.APIBase = apiBase
	a.rt.APIKey = apiKey

	return nil
}

// Stop signals the running agent to exit and blocks until Run has returned
// or ctx is cancelled. Returns ctx.Err() if ctx is cancelled before Run
// finishes. A no-op if the agent is not running.
func (a *Agent) Stop(ctx context.Context) error {
	a.proc.stop()
	a.closeClient()

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

// Remove deletes the agent's workspace directory. Callers must Stop first;
// Remove does not stop a running agent. Returns any filesystem error.
func (a *Agent) Remove(ctx context.Context) error {
	a.status.Set(executor.StatusRemoving)
	a.closeClient()

	if a.fs == nil {
		a.status.Set(executor.StatusRemoved)

		return nil
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if err := util.RemoveAll(a.fs, "."); err != nil {
		return err
	}

	a.status.Set(executor.StatusRemoved)

	return nil
}

func (a *Agent) setRan(ch chan struct{}) {
	a.proc.mu.Lock()
	a.ran = ch
	a.proc.mu.Unlock()
}

func (a *Agent) getRan() chan struct{} {
	a.proc.mu.Lock()
	defer a.proc.mu.Unlock()

	return a.ran
}

// initialize materializes the workspace and builds the command function.
// Serialised; concurrent callers wait on initMu. Successful inits are
// cached (initialized=true) and skipped on subsequent calls. Failures
// are NOT cached — the next caller retries materialize, so a fresh
// providers.yaml can unstick a model_error agent on the next Start.
func (a *Agent) initialize(ctx context.Context) error {
	a.initMu.Lock()
	defer a.initMu.Unlock()

	if a.initialized {
		return nil
	}

	rt, err := a.ws.materialize(ctx, a.fs, a.id, a.addr)
	if err != nil {
		return err
	}

	a.cmdFn = a.proc.buildCmdFn(rt, a.fs.Root(), a.daemonURL, a.agentToken)
	a.rt = rt
	a.initialized = true

	return nil
}

// markInitialized lets Provider.Load bind an already-materialized runtime
// onto an Agent, skipping future ws.materialize calls. Restores a.addr
// from the persisted runtime so Prompt can reach the gRPC server.
// Idempotent: subsequent calls are a no-op once initialized is set.
func (a *Agent) markInitialized(rt *executor.Runtime) {
	a.initMu.Lock()
	defer a.initMu.Unlock()

	if a.initialized {
		return
	}

	if rt.Addr != "" && a.addr == "" {
		a.addr = rt.Addr
	}

	a.cmdFn = a.proc.buildCmdFn(rt, a.fs.Root(), a.daemonURL, a.agentToken)
	a.rt = rt
	a.initialized = true
}
