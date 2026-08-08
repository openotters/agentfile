package executor

import (
	"context"
	"errors"
	"sync"
)

// Status is the lifecycle state of an agent.
//
// Pulling and Starting are emitted by the executor; Ready and Working are
// owned by the daemon supervisor (which probes Ready() and tracks in-flight
// RPCs) and pushed in via Set. The tracker does not enforce transitions —
// it records and broadcasts whatever the executor and supervisor set.
type Status uint8

const (
	StatusPulling  Status = iota // image and layers downloading into the local store
	StatusStarting               // materialising the workspace and spawning the runtime
	StatusReady                  // runtime answered the readiness probe; idle
	StatusWorking                // at least one RPC in flight
	StatusStopped                // subprocess or container exited, or user stopped it
	StatusFailed                 // terminal error; see the companion FailureReason
	StatusRemoving               // teardown in progress
	StatusRemoved                // fully gone
)

// String returns the lowercase status name used in logs, the wire format,
// and the `otters ps` STATUS column.
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

// FailureReason narrows StatusFailed to a specific cause, surfaced on the
// wire alongside the status so operators can tell why an agent failed.
type FailureReason uint8

const (
	FailureNone             FailureReason = iota // zero value on non-failed agents
	FailurePull                                  // agent, runtime, or BIN image could not be pulled
	FailureInit                                  // workspace materialise or container create failed
	FailureConfig                                // a required CONFIG value was not provided
	FailureModel                                 // provider or model resolution failed
	FailureReadinessTimeout                      // runtime never answered the readiness probe
	FailureCrashed                               // runtime exited unexpectedly after reaching Ready
)

// String returns the lowercase reason name for the wire field and CLI
// columns; FailureNone renders as the empty string.
func (r FailureReason) String() string {
	switch r {
	case FailureNone:
		return ""
	case FailurePull:
		return "pull"
	case FailureInit:
		return "init"
	case FailureConfig:
		return "config"
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

// ErrStatusClosed is returned by WaitForStatus when the subscription is
// cancelled before a target status is observed.
var ErrStatusClosed = errors.New("status subscription closed")

// statusChanSize buffers each subscriber. Slow subscribers may miss
// intermediate transitions; Get() always returns the latest status.
const statusChanSize = 16

// StatusTracker records an agent's status and FailureReason and broadcasts
// transitions to subscribers. Safe for concurrent use.
type StatusTracker struct {
	mu      sync.Mutex
	status  Status
	failure FailureReason
	nextID  uint64
	subs    map[uint64]chan Status
}

// NewStatusTracker returns a tracker at StatusPulling (the first transition
// every Run goes through).
func NewStatusTracker() *StatusTracker {
	return &StatusTracker{subs: make(map[uint64]chan Status)}
}

// Get returns the current status.
func (t *StatusTracker) Get() Status {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.status
}

// Failure returns the current FailureReason, meaningful only when the status
// is StatusFailed.
func (t *StatusTracker) Failure() FailureReason {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.failure
}

// Set records a status and broadcasts it. Moving out of StatusFailed clears
// the FailureReason.
func (t *StatusTracker) Set(s Status) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.setLocked(s, FailureNone)
}

// SetFailure records StatusFailed with a cause and broadcasts it.
func (t *StatusTracker) SetFailure(reason FailureReason) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.setLocked(StatusFailed, reason)
}

// SetUnless records s unless the current status is one of the guard values,
// and reports whether it did. It lets a caller set a status without clobbering
// a concurrently-set terminal state — e.g. Run's cleanup sets Stopped unless
// the supervisor already set Failed.
func (t *StatusTracker) SetUnless(s Status, guard ...Status) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, g := range guard {
		if t.status == g {
			return false
		}
	}

	t.setLocked(s, FailureNone)

	return true
}

// setLocked updates state and broadcasts while holding t.mu. Broadcasting
// under the lock is what makes cancel's close race-free: a send can never
// overlap the close, and the sends are non-blocking so the lock is never held
// waiting on a slow reader.
func (t *StatusTracker) setLocked(s Status, reason FailureReason) {
	t.status = s
	if s == StatusFailed {
		t.failure = reason
	} else {
		t.failure = FailureNone
	}

	for _, ch := range t.subs {
		select {
		case ch <- s:
		default:
		}
	}
}

// Subscribe returns a channel of transitions and a cancel function. Callers
// must call cancel (typically via defer) to release the subscription.
func (t *StatusTracker) Subscribe() (<-chan Status, func()) {
	ch := make(chan Status, statusChanSize)

	t.mu.Lock()
	id := t.nextID
	t.nextID++
	t.subs[id] = ch
	t.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()

			delete(t.subs, id)
			close(ch)
		})
	}

	return ch, cancel
}

// WaitForStatus blocks until the observer reaches one of target, or ctx is
// cancelled. It checks the current status first so callers do not race the
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
				return 0, ErrStatusClosed
			}

			if matches(s) {
				return s, nil
			}
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}
