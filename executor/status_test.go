//nolint:testpackage // direct internal access
package executor

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestStatusString(t *testing.T) {
	t.Parallel()

	cases := map[Status]string{
		StatusPulling:  "pulling",
		StatusStarting: "starting",
		StatusReady:    "ready",
		StatusWorking:  "working",
		StatusStopped:  "stopped",
		StatusFailed:   "failed",
		StatusRemoving: "removing",
		StatusRemoved:  "removed",
		Status(99):     "unknown",
	}

	for s, want := range cases {
		if got := s.String(); got != want {
			t.Fatalf("Status(%d).String() = %q, want %q", s, got, want)
		}
	}
}

func TestFailureReasonString(t *testing.T) {
	t.Parallel()

	cases := map[FailureReason]string{
		FailureNone:             "",
		FailurePull:             "pull",
		FailureInit:             "init",
		FailureConfig:           "config",
		FailureModel:            "model",
		FailureReadinessTimeout: "readiness_timeout",
		FailureCrashed:          "crashed",
		FailureReason(99):       "unknown",
	}

	for r, want := range cases {
		if got := r.String(); got != want {
			t.Fatalf("FailureReason(%d).String() = %q, want %q", r, got, want)
		}
	}
}

func TestStatusTracker_GetSet(t *testing.T) {
	t.Parallel()

	tr := NewStatusTracker()

	// The zero value is StatusPulling — the first transition every
	// Run() emits — so fresh trackers read pulling without an
	// explicit Set.
	if got := tr.Get(); got != StatusPulling {
		t.Fatalf("default Get = %v, want StatusPulling", got)
	}

	tr.Set(StatusReady)
	if got := tr.Get(); got != StatusReady {
		t.Fatalf("after Set Get = %v, want StatusReady", got)
	}

	// Moving back out of Failed clears the FailureReason — Set
	// without a reason is always intentional.
	tr.SetFailure(FailurePull)
	if tr.Get() != StatusFailed || tr.Failure() != FailurePull {
		t.Fatalf("SetFailure didn't carry: status=%v reason=%v",
			tr.Get(), tr.Failure())
	}

	tr.Set(StatusStarting)
	if tr.Failure() != FailureNone {
		t.Fatalf("Set didn't clear failure: %v", tr.Failure())
	}
}

func TestStatusTracker_Subscribe(t *testing.T) {
	t.Parallel()

	tr := NewStatusTracker()
	ch, cancel := tr.Subscribe()

	tr.Set(StatusReady)
	tr.Set(StatusStopped)

	seen := []Status{<-ch, <-ch}
	if seen[0] != StatusReady || seen[1] != StatusStopped {
		t.Fatalf("subscribe transitions = %v, want [Ready, Stopped]", seen)
	}

	cancel()
	if _, open := <-ch; open {
		t.Fatal("channel still open after cancel")
	}

	tr.Set(StatusRemoved) // no-op for this subscriber; must not panic
	cancel()              // idempotent
}

func TestStatusTracker_SetFailureBroadcasts(t *testing.T) {
	t.Parallel()

	tr := NewStatusTracker()
	ch, cancel := tr.Subscribe()
	defer cancel()

	tr.SetFailure(FailureModel)

	select {
	case s := <-ch:
		if s != StatusFailed {
			t.Fatalf("subscriber saw %v, want StatusFailed", s)
		}
	case <-time.After(time.Second):
		t.Fatal("SetFailure did not broadcast")
	}

	if tr.Failure() != FailureModel {
		t.Fatalf("Failure() = %v, want FailureModel", tr.Failure())
	}
}

// trackerAdapter lets us drive WaitForStatus directly against StatusTracker
// without needing a full agent.Agent implementation. StatusObserver wants
// `Status()` (not `Get`) so we wrap the tracker's Get behind that name.
type trackerAdapter struct{ *StatusTracker }

func (t trackerAdapter) Status() Status               { return t.Get() }
func (t trackerAdapter) FailureReason() FailureReason { return t.Failure() }

func (t trackerAdapter) SubscribeStatus() (<-chan Status, func()) {
	return t.Subscribe()
}

func TestWaitForStatus_AlreadyAtTarget(t *testing.T) {
	t.Parallel()

	tr := NewStatusTracker()
	tr.Set(StatusReady)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	got, err := WaitForStatus(ctx, trackerAdapter{tr}, StatusReady, StatusStopped)
	if err != nil {
		t.Fatalf("WaitForStatus err = %v", err)
	}

	if got != StatusReady {
		t.Fatalf("got = %v, want Ready", got)
	}
}

func TestWaitForStatus_TransitionsIn(t *testing.T) {
	t.Parallel()

	tr := NewStatusTracker()

	done := make(chan Status, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		s, err := WaitForStatus(ctx, trackerAdapter{tr}, StatusReady)
		if err != nil {
			t.Errorf("WaitForStatus err = %v", err)
		}

		done <- s
	}()

	// Give the waiter time to subscribe before we transition.
	time.Sleep(20 * time.Millisecond)
	tr.Set(StatusReady)

	select {
	case s := <-done:
		if s != StatusReady {
			t.Fatalf("got %v, want Ready", s)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitForStatus did not return")
	}
}

func TestWaitForStatus_ContextCancel(t *testing.T) {
	t.Parallel()

	tr := NewStatusTracker() // stays at StatusPulling (the zero value)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := WaitForStatus(ctx, trackerAdapter{tr}, StatusStopped)
	if err == nil {
		t.Fatal("WaitForStatus returned nil err despite ctx timeout")
	}
}

func TestStatusTracker_ConcurrentSetAndCancel(t *testing.T) {
	t.Parallel()

	// Before the fix, Set broadcast outside the lock while cancel closed the
	// channel under it, so a Set racing a cancel could send on a closed
	// channel and panic. This hammers that interleaving; -race and a plain
	// run must both stay panic-free.
	tr := NewStatusTracker()

	var wg sync.WaitGroup

	for range 50 {
		wg.Add(2)

		go func() {
			defer wg.Done()

			ch, cancel := tr.Subscribe()
			go func() {
				for range ch { //nolint:revive // draining until close
				}
			}()
			cancel()
		}()

		go func() {
			defer wg.Done()

			tr.Set(StatusStarting)
			tr.SetFailure(FailureCrashed)
			tr.Set(StatusStopped)
		}()
	}

	wg.Wait()
}

func TestStatusTracker_SetUnless(t *testing.T) {
	t.Parallel()

	tr := NewStatusTracker()
	tr.SetFailure(FailureCrashed)

	if tr.SetUnless(StatusStopped, StatusFailed) {
		t.Error("SetUnless should not overwrite a guarded status")
	}

	if tr.Get() != StatusFailed || tr.Failure() != FailureCrashed {
		t.Errorf("guarded status changed: %v/%v", tr.Get(), tr.Failure())
	}

	if !tr.SetUnless(StatusStopped, StatusRemoved) {
		t.Error("SetUnless should set when no guard matches")
	}

	if tr.Get() != StatusStopped {
		t.Errorf("status = %v, want stopped", tr.Get())
	}
}
