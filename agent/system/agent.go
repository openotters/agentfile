// Package system implements agent.Provider using local OS processes and a
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

	"github.com/openotters/agentfile/agent"
)

// Agent implements agent.Agent using local OS processes.
type Agent struct {
	id     uuid.UUID
	fs     billy.Filesystem
	ws     workspace
	proc   process
	rt     *agent.AgentRuntime
	addr   string
	cmdFn  cmdFunc
	dialer Dialer

	initOnce sync.Once
	initErr  error
	ran      chan struct{}

	clientMu   sync.Mutex
	clientConn *grpc.ClientConn

	status *agent.StatusTracker
}

// NewAgent creates a system agent.
func NewAgent(id uuid.UUID, fs billy.Filesystem, opts ...AgentOption) *Agent {
	a := &Agent{
		id: id,
		fs: fs,
		proc: process{
			executor: defaultExecutor{},
			stdout:   os.Stdout,
			stderr:   os.Stderr,
		},
		dialer: defaultDialer{},
		status: agent.NewStatusTracker(),
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
func (a *Agent) Runtime() *agent.AgentRuntime { return a.rt }

// Addr returns the loopback host:port the runtime subprocess will
// bind (reserved by the Provider's LoopbackAllocator at Create).
// Empty on agents constructed without WithAddr and before Prepare.
func (a *Agent) Addr() string { return a.addr }

// Status returns the current lifecycle state. Safe for concurrent
// access with Start/Stop/Remove.
func (a *Agent) Status() agent.Status { return a.status.Get() }

// SubscribeStatus returns a channel of status transitions and a
// cancel function. Sends are non-blocking; slow subscribers may miss
// intermediate transitions — call Status() to resync. Always call
// cancel to avoid leaking the subscription.
func (a *Agent) SubscribeStatus() (<-chan agent.Status, func()) {
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
func (a *Agent) Prepare(ctx context.Context) error {
	if err := a.initialize(ctx); err != nil {
		if errors.Is(err, ErrPull) {
			a.status.Set(agent.StatusPullError)
		} else {
			a.status.Set(agent.StatusInitError)
		}

		return err
	}

	return nil
}

// Run materializes the workspace (if needed), starts the serve process, and blocks.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.Prepare(ctx); err != nil {
		return err
	}

	a.status.Set(agent.StatusRunning)

	ran := make(chan struct{})
	a.setRan(ran)

	defer func() {
		close(ran)
		a.status.Set(agent.StatusStopped)
	}()

	return a.proc.serve(ctx, a.cmdFn)
}

// Start re-runs a stopped agent on the already-materialized workspace.
// Blocks until the subprocess exits or ctx is cancelled (same contract as
// Run). Returns an error if the agent is already running or has been
// removed. The loopback address from the original Create is reused.
func (a *Agent) Start(ctx context.Context) error {
	switch a.Status() {
	case agent.StatusRunning:
		return fmt.Errorf("agent already running")
	case agent.StatusRemoving, agent.StatusRemoved:
		return fmt.Errorf("agent removed")
	case agent.StatusCreated, agent.StatusStopped, agent.StatusInitError, agent.StatusPullError:
		// startable: fall through to Run
	}

	return a.Run(ctx)
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
	a.status.Set(agent.StatusRemoving)
	a.closeClient()

	if a.fs == nil {
		a.status.Set(agent.StatusRemoved)

		return nil
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if err := util.RemoveAll(a.fs, "."); err != nil {
		return err
	}

	a.status.Set(agent.StatusRemoved)

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
// Serialized and idempotent via sync.Once; concurrent callers observe the
// same initErr.
func (a *Agent) initialize(ctx context.Context) error {
	a.initOnce.Do(func() {
		rt, err := a.ws.materialize(ctx, a.fs, a.id, a.addr)
		if err != nil {
			a.initErr = err

			return
		}

		a.cmdFn = a.proc.buildCmdFn(rt, a.fs.Root())
		a.rt = rt
	})

	return a.initErr
}

// markInitialized lets Provider.Load bind an already-materialized runtime
// onto an Agent, skipping future ws.materialize calls. Restores a.addr
// from the persisted runtime so Prompt can reach the gRPC server.
func (a *Agent) markInitialized(rt *agent.AgentRuntime) {
	a.initOnce.Do(func() {
		if rt.Addr != "" && a.addr == "" {
			a.addr = rt.Addr
		}

		a.cmdFn = a.proc.buildCmdFn(rt, a.fs.Root())
		a.rt = rt
	})
}
