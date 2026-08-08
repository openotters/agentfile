package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/google/uuid"

	"github.com/openotters/agentfile/executor/system"
	"github.com/openotters/agentfile/oci"
)

// TestSystemExecutor_E2E spawns the runtime as a real host process, confirms it
// materialises and binds its server, and shuts it down cleanly.
//
// Not parallel: it binds loopback ports and spawns a real subprocess.
func TestSystemExecutor_E2E(t *testing.T) {
	apiBase := requireEnv(t, "OTTERS_E2E_API_BASE")
	runtimeBin := requireEnv(t, "OTTERS_E2E_RUNTIME_BIN")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	store, ref := buildSmokeAgent(t, "system-e2e", "ghcr.io/openotters/runtime:latest")
	addr := reserveLoopbackAddr(t)

	a := system.NewAgent(uuid.New(), osfs.New(t.TempDir()),
		system.WithStore(store),
		system.WithReference(ref),
		system.WithStaticModelResolver(apiBase, "dummy-key"),
		// The local binary replaces the RUNTIME ref; NoopPuller stubs the
		// (unused) tree copy so no registry is touched.
		system.WithAgentLocalRuntime(runtimeBin),
		system.WithAgentPuller(oci.NoopPuller()),
		system.WithAddr(addr),
	)

	runErr := make(chan error, 1)
	go func() { runErr <- a.Run(ctx) }()

	waitListening(t, addr, 30*time.Second)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer stopCancel()

	if err := a.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}

	if err := <-runErr; err != nil {
		t.Fatalf("run exited with error: %v", err)
	}
}
