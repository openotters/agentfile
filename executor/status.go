package executor

import (
	"context"
	"sync"
)

// Status represents the lifecycle state of an agent.
type Status uint8

const (
	StatusCreated Status = iota
	StatusRunning
	StatusStopped
	StatusRemoving
	StatusRemoved
	StatusInitError
	StatusPullError
	StatusModelError
)

// String returns the lowercase name of the Status — used for logs and
// the `otters ps` STATUS column. Unknown numeric values render as
// "unknown" rather than panicking.
func (s Status) String() string {
	switch s {
	case StatusCreated:
		return "created"
	case StatusRunning:
		return "running"
	case StatusStopped:
		return "stopped"
	case StatusRemoving:
		return "removing"
	case StatusRemoved:
		return "removed"
	case StatusInitError:
		return "init_error"
	case StatusPullError:
		return "pull_error"
	case StatusModelError:
		return "model_error"
	default:
		return "unknown"
	}
}

// statusChanSize is the per-subscriber buffer. Slow subscribers may miss
// intermediate transitions; only the latest Status is guaranteed observable
// via Get().
const statusChanSize = 16

// StatusTracker manages agent status and broadcasts transitions to subscribers.
type StatusTracker struct {
	mu     sync.Mutex
	status Status
	nextID uint64
	subs   map[uint64]chan Status
}

// NewStatusTracker creates a new status tracker.
func NewStatusTracker() *StatusTracker {
	return &StatusTracker{subs: make(map[uint64]chan Status)}
}

// Get returns the current status.
func (t *StatusTracker) Get() Status {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.status
}

// Set updates the status and broadcasts to all subscribers.
// Sends are non-blocking; a slow subscriber's channel may drop values.
func (t *StatusTracker) Set(s Status) {
	t.mu.Lock()
	t.status = s
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
