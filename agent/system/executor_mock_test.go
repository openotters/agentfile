package system_test

import (
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/google/uuid"

	"github.com/openotters/agentfile/agent/system"
	mocksystem "github.com/openotters/agentfile/mocks/system"
)

// This test lives in package system_test (not system) so it can import
// mocks/system without creating an import cycle. It asserts that
// WithExecutor correctly wires the mock-generated Executor into the
// agent — a smoke test for the public injection path downstream
// packages will use.
func TestWithExecutor_InjectsMock(t *testing.T) {
	t.Parallel()

	mockExec := mocksystem.NewMockExecutor(t)
	// No calls expected — we're verifying the option plumbing, not
	// triggering subprocess creation.

	a := system.NewAgent(uuid.New(), memfs.New(), system.WithExecutor(mockExec))

	if a == nil {
		t.Fatal("NewAgent returned nil")
	}

	// No direct way to read back the injected executor from the public
	// API — the proof that it plumbed through is the absence of
	// "unexpected call" failures from the mock when we don't spawn,
	// and the positive assertion lands in the internal exec_test.go
	// flow where we drive process.serve directly with a stub.
}
