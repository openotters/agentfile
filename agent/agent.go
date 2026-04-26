// Package agent defines the lifecycle contract for a running agent
// and the shared status tracker used by concrete backends.
package agent

import (
	"context"

	"github.com/google/uuid"
)

// StatusObserver provides status tracking.
type StatusObserver interface {
	// Status returns the current state.
	Status() Status
	// SubscribeStatus returns a channel of status transitions and a cancel
	// function that closes the channel. Sends are non-blocking: slow
	// subscribers may miss intermediate transitions; call Status() to
	// resync. Always call cancel to avoid leaking the subscription.
	SubscribeStatus() (<-chan Status, func())
}

// Agent defines the lifecycle contract for a running agent.
//
// Prepare materializes the workspace synchronously without starting the
// runtime process. It's idempotent and safe to call concurrently; repeat
// calls return the same error. Callers who want init errors to surface
// before any Run goroutine is spawned should call Prepare first.
//
// Run starts the runtime subprocess, blocking until it exits or ctx is
// cancelled. Run calls Prepare internally if the workspace has not yet
// been materialized; initialization errors are returned directly and
// also surfaced via Status (StatusInitError / StatusPullError /
// StatusModelError).
//
// Start re-runs a previously-stopped agent on the already-materialized
// workspace. Blocks until the subprocess exits or ctx is cancelled, same
// contract as Run. Returns an error if the agent is already running or
// has been removed. Reuses the same workspace and loopback address.
//
// Stop signals the running agent to exit and blocks until Run has returned
// or ctx is cancelled. Calling Stop on a non-running agent is a no-op.
//
// Remove deletes the agent's on-disk state. Callers must Stop first and
// wait for Run to return before Remove; Remove transitions Status through
// StatusRemoving → StatusRemoved on success.
type Agent interface {
	UUID() uuid.UUID
	Runtime() *AgentRuntime
	Prepare(ctx context.Context) error
	Run(ctx context.Context) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Remove(ctx context.Context) error

	StatusObserver
}
