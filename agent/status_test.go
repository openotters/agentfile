//nolint:testpackage // direct internal access
package agent

import (
	"context"
	"testing"
	"time"
)

func TestStatusString(t *testing.T) {
	t.Parallel()

	cases := map[Status]string{
		StatusCreated:   "created",
		StatusRunning:   "running",
		StatusStopped:   "stopped",
		StatusRemoving:  "removing",
		StatusRemoved:   "removed",
		StatusInitError: "init_error",
		StatusPullError: "pull_error",
		Status(99):      "unknown",
	}

	for s, want := range cases {
		if got := s.String(); got != want {
			t.Fatalf("Status(%d).String() = %q, want %q", s, got, want)
		}
	}
}

func TestStatusTracker_GetSet(t *testing.T) {
	t.Parallel()

	tr := NewStatusTracker()

	if got := tr.Get(); got != StatusCreated {
		t.Fatalf("default Get = %v, want StatusCreated", got)
	}

	tr.Set(StatusRunning)
	if got := tr.Get(); got != StatusRunning {
		t.Fatalf("after Set Get = %v, want StatusRunning", got)
	}
}

func TestStatusTracker_Subscribe(t *testing.T) {
	t.Parallel()

	tr := NewStatusTracker()
	ch, cancel := tr.Subscribe()

	tr.Set(StatusRunning)
	tr.Set(StatusStopped)

	seen := []Status{<-ch, <-ch}
	if seen[0] != StatusRunning || seen[1] != StatusStopped {
		t.Fatalf("subscribe transitions = %v, want [Running, Stopped]", seen)
	}

	cancel()
	if _, open := <-ch; open {
		t.Fatal("channel still open after cancel")
	}

	tr.Set(StatusRemoved) // no-op for this subscriber; must not panic
	cancel()              // idempotent
}

// trackerAdapter lets us drive WaitForStatus directly against StatusTracker
// without needing a full agent.Agent implementation. StatusObserver wants
// `Status()` (not `Get`) so we wrap the tracker's Get behind that name.
type trackerAdapter struct{ *StatusTracker }

func (t trackerAdapter) Status() Status { return t.Get() }

func (t trackerAdapter) SubscribeStatus() (<-chan Status, func()) {
	return t.Subscribe()
}

func TestWaitForStatus_AlreadyAtTarget(t *testing.T) {
	t.Parallel()

	tr := NewStatusTracker()
	tr.Set(StatusRunning)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	got, err := WaitForStatus(ctx, trackerAdapter{tr}, StatusRunning, StatusStopped)
	if err != nil {
		t.Fatalf("WaitForStatus err = %v", err)
	}

	if got != StatusRunning {
		t.Fatalf("got = %v, want Running", got)
	}
}

func TestWaitForStatus_TransitionsIn(t *testing.T) {
	t.Parallel()

	tr := NewStatusTracker()

	done := make(chan Status, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		s, err := WaitForStatus(ctx, trackerAdapter{tr}, StatusRunning)
		if err != nil {
			t.Errorf("WaitForStatus err = %v", err)
		}

		done <- s
	}()

	// Give the waiter time to subscribe before we transition.
	time.Sleep(20 * time.Millisecond)
	tr.Set(StatusRunning)

	select {
	case s := <-done:
		if s != StatusRunning {
			t.Fatalf("got %v, want Running", s)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitForStatus did not return")
	}
}

func TestWaitForStatus_ContextCancel(t *testing.T) {
	t.Parallel()

	tr := NewStatusTracker() // stays at StatusCreated

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := WaitForStatus(ctx, trackerAdapter{tr}, StatusStopped)
	if err == nil {
		t.Fatal("WaitForStatus returned nil err despite ctx timeout")
	}
}
