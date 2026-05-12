package executor

import (
	"context"
	"sync"
)

// Status represents the lifecycle state of an agent.
//
// The state machine:
//
//	Pulling ── (cache hit) ──┐
//	    │                    │
//	    ▼                    ▼
//	Starting ────────────► Failed (carries a FailureReason)
//	    │                    ▲
//	    ▼                    │
//	Ready ◄──────────────────┤
//	    │ ▲                  │
//	    ▼ │                  │
//	Working ─────────────────┘
//	    │
//	    ▼
//	Stopped ── (Start) ──► Pulling
//	    │
//	    ▼
//	Removing ──► Removed
//
// Pulling and Starting are emitted by the executor; Ready and Working
// are owned by the daemon supervisor (probes Ready() / tracks
// in-flight RPCs) and pushed into the tracker via Set.
type Status uint8

const (
	// StatusPulling — image / layers being downloaded into the local
	// store. The slow phase when the cache is cold; a hit flips to
	// StatusStarting almost immediately.
	StatusPulling Status = iota

	// StatusStarting — pull is done; workspace materialise, model
	// resolution, and subprocess / container spawn happen here. The
	// runtime has not yet acknowledged a readiness probe.
	StatusStarting

	// StatusReady — runtime answered the readiness probe. Idle,
	// accepting RPCs. Set by the daemon supervisor, not the executor.
	StatusReady

	// StatusWorking — at least one chat turn or async-job RPC is in
	// flight to this agent. Tracked daemon-side over the in-flight
	// count and flipped back to Ready when the count drops to 0.
	StatusWorking

	// StatusStopped — user-initiated stop, or the subprocess /
	// container exited (cleanly or otherwise). The daemon decides
	// whether to retry (re-Run) or to flip to Failed+Crashed based on
	// the exit context.
	StatusStopped

	// StatusFailed — terminal error. The companion FailureReason on
	// the tracker carries the specific cause; surface it on the wire
	// so operators see why.
	StatusFailed

	// StatusRemoving — cleanup in progress (workspace delete, container
	// teardown).
	StatusRemoving

	// StatusRemoved — agent is fully gone; the row may still exist
	// briefly before its persisted record is deleted.
	StatusRemoved
)

// String returns the lowercase name of the Status — used for logs,
// the daemon's wire format, and the `otters ps` STATUS column.
// Unknown numeric values render as "unknown" rather than panicking.
func (s Status) String() string {
	switch s {
	case StatusPulling:
		return "pulling"
	case StatusStarting:
		return "starting"
	case StatusReady:
		return "ready"
	case StatusWorking:
		return "working"
	case StatusStopped:
		return "stopped"
	case StatusFailed:
		return "failed"
	case StatusRemoving:
		return "removing"
	case StatusRemoved:
		return "removed"
	default:
		return "unknown"
	}
}

// FailureReason narrows StatusFailed to a specific cause. The
// daemon surfaces it on the wire alongside the status string so
// dashboards / the CLI can explain why the agent is in Failed.
type FailureReason uint8

const (
	// FailureNone is the zero value carried by non-Failed agents.
	FailureNone FailureReason = iota

	// FailurePull — the image (agent / runtime / a BIN) could not be
	// pulled into the local store. Network, registry-auth, or
	// missing-tag.
	FailurePull

	// FailureInit — workspace materialise or container create failed.
	// Filesystem write, mount target collision, exec format errors.
	FailureInit

	// FailureModel — provider / model resolution failed. Missing
	// provider config, bad API base, key rotation gone wrong.
	FailureModel

	// FailureReadinessTimeout — the runtime subprocess started but
	// did not answer the daemon's Ready() probe within the timeout.
	FailureReadinessTimeout

	// FailureCrashed — the subprocess / container exited unexpectedly
	// after having reached Ready. The daemon marks Failed+Crashed
	// rather than retrying, since this is usually a code or config
	// bug, not a transient blip.
	FailureCrashed
)

// String returns the lowercase name of the FailureReason. Used in
// the wire field (`AgentInfo.failure_reason`) and in CLI columns.
// FailureNone returns the empty string so callers can render it as
// "no failure" without a special-case branch.
func (r FailureReason) String() string {
	switch r {
	case FailureNone:
		return ""
	case FailurePull:
		return "pull"
	case FailureInit:
		return "init"
	case FailureModel:
		return "model"
	case FailureReadinessTimeout:
		return "readiness_timeout"
	case FailureCrashed:
		return "crashed"
	default:
		return "unknown"
	}
}

// statusChanSize is the per-subscriber buffer. Slow subscribers may miss
// intermediate transitions; only the latest Status is guaranteed observable
// via Get().
const statusChanSize = 16

// StatusTracker manages agent status (and its companion FailureReason)
// and broadcasts transitions to subscribers.
type StatusTracker struct {
	mu      sync.Mutex
	status  Status
	failure FailureReason
	nextID  uint64
	subs    map[uint64]chan Status
}

// NewStatusTracker creates a new status tracker. The zero value is
// StatusPulling (the first transition any Run() goes through), so
// freshly-created Agents read pulling before they emit anything.
func NewStatusTracker() *StatusTracker {
	return &StatusTracker{subs: make(map[uint64]chan Status)}
}

// Get returns the current status.
func (t *StatusTracker) Get() Status {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.status
}

// Failure returns the current FailureReason. Meaningful only when
// Get() == StatusFailed; otherwise the zero value (FailureNone).
func (t *StatusTracker) Failure() FailureReason {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.failure
}

// Set updates the status and broadcasts to all subscribers. Clears
// any prior FailureReason — moving out of Failed is always intentional.
// Sends are non-blocking; a slow subscriber's channel may drop values.
func (t *StatusTracker) Set(s Status) {
	t.mu.Lock()
	t.status = s
	if s != StatusFailed {
		t.failure = FailureNone
	}
	subs := make([]chan Status, 0, len(t.subs))
	for _, ch := range t.subs {
		subs = append(subs, ch)
	}
	t.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- s:
		default:
		}
	}
}

// SetFailure marks the agent failed with a specific reason. Equivalent
// to Set(StatusFailed) but carries the cause for downstream surfaces
// (the daemon's wire field, the dashboard's badge tooltip, the CLI's
// FAILURE column).
func (t *StatusTracker) SetFailure(reason FailureReason) {
	t.mu.Lock()
	t.status = StatusFailed
	t.failure = reason
	subs := make([]chan Status, 0, len(t.subs))
	for _, ch := range t.subs {
		subs = append(subs, ch)
	}
	t.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- StatusFailed:
		default:
		}
	}
}

// WaitForStatus blocks until the observer reaches one of target or ctx is
// cancelled. Returns the observed status on hit, or ctx.Err() otherwise.
// The current status is checked first, so callers do not race the
// transition they are waiting for.
func WaitForStatus(ctx context.Context, o StatusObserver, target ...Status) (Status, error) {
	matches := func(s Status) bool {
		for _, t := range target {
			if s == t {
				return true
			}
		}

		return false
	}

	ch, cancel := o.SubscribeStatus()
	defer cancel()

	if s := o.Status(); matches(s) {
		return s, nil
	}

	for {
		select {
		case s, ok := <-ch:
			if !ok {
				return 0, ctx.Err()
			}

			if matches(s) {
				return s, nil
			}
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

// Subscribe returns a channel of status transitions and a cancel function.
// Cancel closes the channel and removes the subscription. Callers must call
// cancel to avoid leaking the subscription; typical use is `defer cancel()`.
func (t *StatusTracker) Subscribe() (<-chan Status, func()) {
	ch := make(chan Status, statusChanSize)

	t.mu.Lock()
	id := t.nextID
	t.nextID++
	t.subs[id] = ch
	t.mu.Unlock()

	cancel := func() {
		t.mu.Lock()
		sub, ok := t.subs[id]
		if ok {
			delete(t.subs, id)
		}
		t.mu.Unlock()

		if ok {
			close(sub)
		}
	}

	return ch, cancel
}
