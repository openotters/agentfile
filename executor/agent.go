// Package agent defines the lifecycle contract for a running agent
// and the shared status tracker used by concrete backends.
package executor

import (
	"context"

	"github.com/google/uuid"
)

// StatusObserver provides status tracking.
type StatusObserver interface {
	// Status returns the current state.
	Status() Status
	// FailureReason returns the cause when Status() == StatusFailed.
	// Returns FailureNone for non-failed states; callers can render
	// the zero value as the empty string via FailureReason.String().
	FailureReason() FailureReason
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
// also surfaced via Status (StatusFailed with a FailureReason of
// FailurePull / FailureInit / FailureModel).
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
	Runtime() *Runtime
	Prepare(ctx context.Context) error
	Run(ctx context.Context) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Remove(ctx context.Context) error

	// StatusTracker exposes the underlying tracker so the daemon
	// supervisor can drive transitions it owns (Ready after the
	// readiness probe answers, Working ↔ Ready around in-flight RPCs,
	// Failed+FailureReadinessTimeout on probe timeout, Failed+
	// FailureCrashed on unexpected exit). Executors mutate their own
	// status through this same tracker for the Pulling / Starting /
	// Stopped / Failed transitions they own.
	StatusTracker() *StatusTracker

	// Probe issues a single readiness check against the running
	// runtime. Returns nil when the runtime answered Ready=true.
	// Returns a non-nil error when the dial fails, the call returns
	// Unavailable, or ctx expires.
	//
	// Used by the daemon supervisor to gate the Starting → Ready
	// transition. Implementations should not retry internally — the
	// caller owns the backoff and the overall timeout.
	Probe(ctx context.Context) error

	// Exec runs a BIN command in this agent's spawn env: same image,
	// same BIN namespace on PATH, agent workspace as cwd. Implementations
	// MUST cleanly terminate the underlying execution when ctx is
	// cancelled — no orphaned processes / no zombie containers.
	// Returns when the underlying execution exits or is killed.
	//
	// `bin` is a name (e.g. "sh", "jq") resolved via PATH inside the
	// spawn env, NOT a host filesystem path. Unknown names surface as
	// ExecResult.Err so the caller can distinguish "BIN not declared in
	// the agent's image" from "BIN ran and exited non-zero."
	Exec(ctx context.Context, bin string, args []string, stdin string) ExecResult

	StatusObserver
}

// ExecResult is the captured outcome of one BIN invocation via
// Agent.Exec. Stdout / Stderr are best-effort: if cancellation
// truncates the run, what was captured up to that point is returned
// alongside ctx.Err() in Err.
type ExecResult struct {
	Stdout, Stderr string
	ExitCode       int

	// Err is set for spawn failures (BIN not in PATH inside the
	// sandbox, container gone, sandbox unavailable) and for
	// ctx-cancellation (ctx.Err() flows through). Nil when the BIN
	// ran to completion, regardless of its exit code.
	Err error

	// Handle is a backend-specific identifier the daemon uses for
	// boot-time ghost cleanup: a PID for the system backend, a
	// container ID for docker. Empty when the spawn never happened.
	Handle string
}
