package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/google/uuid"
	"oras.land/oras-go/v2"

	"github.com/openotters/agentfile/executor/docker"
	"github.com/openotters/agentfile/model"
	"github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
)

// TestDockerExecutor_E2E spawns the runtime in a real Linux container (runtime
// delivered via a type=image mount), confirms it materialises and binds its
// server, and tears it down cleanly.
//
// Not parallel: it talks to the shared Docker daemon and binds a loopback port.
func TestDockerExecutor_E2E(t *testing.T) {
	apiBase := requireEnv(t, "OTTERS_E2E_API_BASE")
	runtimeImage := requireEnv(t, "OTTERS_E2E_RUNTIME_IMAGE")

	// Inside the container, localhost is the container itself; the host's
	// loopback is reached via host.docker.internal (the executor adds the
	// host-gateway mapping).
	dockerAPIBase := os.Getenv("OTTERS_E2E_DOCKER_API_BASE")
	if dockerAPIBase == "" {
		dockerAPIBase = strings.NewReplacer(
			"localhost", "host.docker.internal",
			"127.0.0.1", "host.docker.internal",
		).Replace(apiBase)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	store, ref := buildSmokeAgent(t, "docker-e2e", runtimeImage)

	// The agent root must live under $HOME: Colima (and most macOS
	// docker VMs) only share the home directory, so a bind mount from
	// t.TempDir() (/var/folders/…) would come up empty in the
	// container. Hand-rolled tempdir + t.Cleanup instead of t.TempDir
	// for exactly that reason.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	cacheDir := filepath.Join(home, ".cache")
	if mkErr := os.MkdirAll(cacheDir, 0o755); mkErr != nil {
		t.Fatal(mkErr)
	}

	workDir, err := os.MkdirTemp(cacheDir, "agentfile-docker-e2e-*")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(workDir) })

	storeFor := func(spec.Reference) oras.ReadOnlyTarget { return store }

	provider, err := docker.NewProvider(osfs.New(workDir), storeFor,
		docker.WithModelResolver(model.StaticResolver(dockerAPIBase, "dummy-key")),
		// The runtime image is local; nothing to pull into the tree.
		docker.WithPuller(oci.NoopPuller()),
	)
	if err != nil {
		t.Skipf("docker daemon unavailable: %v", err)
	}

	t.Cleanup(func() { _ = provider.Close() })

	agent, err := provider.Create(ctx, uuid.New(), ref)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	runErr := make(chan error, 1)
	go func() { runErr <- agent.Run(ctx) }()

	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer stopCancel()
		_ = agent.Remove(stopCtx)
	})

	waitListening(t, agent.Addr(), 60*time.Second)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer stopCancel()

	if stopErr := agent.Stop(stopCtx); stopErr != nil {
		t.Fatalf("stop: %v", stopErr)
	}
}
