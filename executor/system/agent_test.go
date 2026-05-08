//nolint:testpackage // direct internal access
package system

import (
	"bytes"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/google/uuid"

	"github.com/openotters/agentfile/executor"
)

func TestNewAgent_Defaults(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	a := NewAgent(id, memfs.New())

	if a.UUID() != id {
		t.Fatalf("UUID() = %v, want %v", a.UUID(), id)
	}

	if a.Addr() != "" {
		t.Fatalf("Addr() = %q, want empty", a.Addr())
	}

	if a.Runtime() != nil {
		t.Fatalf("Runtime() = %v, want nil before Prepare", a.Runtime())
	}

	if got := a.Status(); got != executor.StatusCreated {
		t.Fatalf("Status() = %v, want StatusCreated", got)
	}
}

func TestNewAgent_OptionsApply(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	mounts := []executor.Mount{{Host: "/h", Target: "/t"}}

	a := NewAgent(uuid.New(), memfs.New(),
		WithAddr("127.0.0.1:4242"),
		WithStdout(&stdout),
		WithStderr(&stderr),
		WithMounts(mounts),
	)

	if a.Addr() != "127.0.0.1:4242" {
		t.Fatalf("WithAddr not applied: %q", a.Addr())
	}

	if a.proc.stdout != &stdout {
		t.Fatalf("WithStdout not applied")
	}

	if a.proc.stderr != &stderr {
		t.Fatalf("WithStderr not applied")
	}

	if len(a.ws.mounts) != 1 || a.ws.mounts[0].Host != "/h" {
		t.Fatalf("WithMounts not applied: %+v", a.ws.mounts)
	}
}

func TestAgent_StatusTrackerWired(t *testing.T) {
	t.Parallel()

	a := NewAgent(uuid.New(), memfs.New())

	a.status.Set(executor.StatusRunning)

	if got := a.Status(); got != executor.StatusRunning {
		t.Fatalf("Status() = %v after Set, want StatusRunning", got)
	}

	ch, cancel := a.SubscribeStatus()
	defer cancel()

	a.status.Set(executor.StatusStopped)
	if got := <-ch; got != executor.StatusStopped {
		t.Fatalf("SubscribeStatus delivered %v, want StatusStopped", got)
	}
}
