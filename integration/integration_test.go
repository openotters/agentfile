// Package integration holds opt-in end-to-end tests that exercise the executor
// boot chain — build → materialize → spawn → the runtime binds its server — on
// both backends against a REAL model API. Talking to the runtime (chat) is the
// orchestrator's concern over its own wire protocol and is out of scope here;
// these tests assert the runtime comes up and shuts down cleanly. They are
// compiled by every `go test ./...` run (so they cannot rot) but skip unless
// the operator provides the external pieces via environment:
//
//	OTTERS_E2E_API_BASE       model API endpoint, e.g. a local meridian
//	                          proxy: http://localhost:3456
//	OTTERS_E2E_RUNTIME_BIN    host-platform runtime binary, e.g.
//	                          go build -o /tmp/runtime-bin ../runtime/cmd/runtime
//	OTTERS_E2E_RUNTIME_IMAGE  linux runtime image for the docker
//	                          executor, binary at /runtime, e.g.
//	                          CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
//	                            go build -o runtime ../runtime/cmd/runtime
//	                          docker build -t local/otters-runtime:e2e .
//	OTTERS_E2E_DOCKER_API_BASE  optional; defaults to OTTERS_E2E_API_BASE
//	                          with localhost rewritten to
//	                          host.docker.internal
//
// Full run:
//
//	OTTERS_E2E_API_BASE=http://localhost:3456 \
//	OTTERS_E2E_RUNTIME_BIN=/tmp/runtime-bin \
//	OTTERS_E2E_RUNTIME_IMAGE=local/otters-runtime:e2e \
//	go test ./integration -v
package integration_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/build"
	"github.com/openotters/agentfile/spec"
)

func requireEnv(t *testing.T, key string) string {
	t.Helper()

	v := os.Getenv(key)
	if v == "" {
		t.Skipf("%s not set — skipping opt-in integration test (see package doc)", key)
	}

	return v
}

// buildSmokeAgent builds a minimal runnable Agentfile (no BINs, no
// network deps) into an in-memory OCI store.
func buildSmokeAgent(t *testing.T, name, runtimeRef string) (*memory.Store, spec.Reference) {
	t.Helper()

	agentfile := `# syntax=openotters/agentfile:1

FROM scratch

NAME ` + name + `
RUNTIME ` + runtimeRef + `
MODEL anthropic/claude-haiku-4-5-20251001

CONTEXT SOUL "E2E smoke personality" <<MARK
You are a terse smoke-test agent. Follow instructions exactly.
MARK

CONFIG max-tokens=100 "Cap output"
`

	dir := t.TempDir()

	path := filepath.Join(dir, "Agentfile")
	if err := os.WriteFile(path, []byte(agentfile), 0o644); err != nil {
		t.Fatal(err)
	}

	store := memory.New()

	ref, err := build.FromFile(context.Background(), path, store)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	return store, ref.Reference
}

// reserveLoopbackAddr grabs a free 127.0.0.1 port and returns the
// address. Tiny TOCTOU window, same trade-off both executors make.
func reserveLoopbackAddr(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	addr := l.Addr().String()
	_ = l.Close()

	return addr
}

// waitListening polls addr until a TCP connection succeeds, proving the runtime
// bound its server after materialise + spawn. Protocol-agnostic — it does not
// speak to the runtime, only confirms it is up.
func waitListening(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = conn.Close()

			return
		}

		time.Sleep(200 * time.Millisecond)
	}

	t.Fatalf("runtime did not start listening on %s within %s", addr, timeout)
}
