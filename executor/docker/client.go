// Package docker implements executor.Provider on top of the Docker
// Engine via the moby/moby Go SDK. Each agent runs as an ephemeral
// container against a small base image (default: distroless/static),
// with the agent's materialised content directory bind-mounted at
// /workspace and the runtime + each BIN image-mounted read-only at
// /opt/runtime / /opt/bins/<name>.
//
// The CLI (`docker`) is intentionally not used. All Docker
// interactions go through the SDK so tests can swap a mock implementing
// the small Client interface defined here.
package docker

import (
	"context"
	"io"
	"os"
	"path/filepath"

	mobyclient "github.com/moby/moby/client"
)

// Client is the strict subset of the moby/moby SDK that the executor
// relies on. Defining it here (rather than depending on the full
// `*mobyclient.Client` directly) keeps the test surface tiny:
// mockery generates a mock that only has to implement the methods
// we genuinely call.
//
// The real implementation is *mobyclient.Client; in tests, swap a
// mock via WithClient(MockClient).
//
// The moby SDK's v0.4.x convention is
// `(ctx, [id,] Options) (Result, error)` — this interface mirrors
// that signature exactly so the real client satisfies it without an
// adapter (the compile-time assertion below enforces that).
type Client interface {
	Info(
		ctx context.Context, opts mobyclient.InfoOptions,
	) (mobyclient.SystemInfoResult, error)
	ServerVersion(
		ctx context.Context, opts mobyclient.ServerVersionOptions,
	) (mobyclient.ServerVersionResult, error)

	ContainerCreate(
		ctx context.Context, opts mobyclient.ContainerCreateOptions,
	) (mobyclient.ContainerCreateResult, error)
	ContainerStart(
		ctx context.Context, id string, opts mobyclient.ContainerStartOptions,
	) (mobyclient.ContainerStartResult, error)
	ContainerStop(
		ctx context.Context, id string, opts mobyclient.ContainerStopOptions,
	) (mobyclient.ContainerStopResult, error)
	ContainerRemove(
		ctx context.Context, id string, opts mobyclient.ContainerRemoveOptions,
	) (mobyclient.ContainerRemoveResult, error)
	ContainerInspect(
		ctx context.Context, id string, opts mobyclient.ContainerInspectOptions,
	) (mobyclient.ContainerInspectResult, error)
	ContainerLogs(
		ctx context.Context, id string, opts mobyclient.ContainerLogsOptions,
	) (mobyclient.ContainerLogsResult, error)
	ContainerList(
		ctx context.Context, opts mobyclient.ContainerListOptions,
	) (mobyclient.ContainerListResult, error)

	ImagePull(
		ctx context.Context, ref string, opts mobyclient.ImagePullOptions,
	) (mobyclient.ImagePullResponse, error)
	ImagePush(
		ctx context.Context, ref string, opts mobyclient.ImagePushOptions,
	) (mobyclient.ImagePushResponse, error)
	ImageList(
		ctx context.Context, opts mobyclient.ImageListOptions,
	) (mobyclient.ImageListResult, error)
	ImageInspect(
		ctx context.Context, imageID string, opts ...mobyclient.ImageInspectOption,
	) (mobyclient.ImageInspectResult, error)
	ImageRemove(
		ctx context.Context, imageID string, opts mobyclient.ImageRemoveOptions,
	) (mobyclient.ImageRemoveResult, error)
	ImageTag(
		ctx context.Context, opts mobyclient.ImageTagOptions,
	) (mobyclient.ImageTagResult, error)
	ImageLoad(
		ctx context.Context, input io.Reader, opts ...mobyclient.ImageLoadOption,
	) (mobyclient.ImageLoadResult, error)
	ImageSave(
		ctx context.Context, imageIDs []string, opts ...mobyclient.ImageSaveOption,
	) (mobyclient.ImageSaveResult, error)

	Close() error
}

// NewClient constructs a real *mobyclient.Client. Endpoint
// resolution order:
//
//  1. DOCKER_HOST env var (and DOCKER_TLS_VERIFY / DOCKER_CERT_PATH /
//     DOCKER_API_VERSION friends, via mobyclient.FromEnv).
//  2. Auto-detected sockets in user-home or runtime dirs that
//     non-privileged Docker installs put their endpoint at:
//     - ~/.colima/default/docker.sock          (Colima default profile)
//     - ~/.docker/run/docker.sock              (Docker Desktop, recent)
//     - $XDG_RUNTIME_DIR/docker.sock           (rootless Linux Docker)
//  3. SDK default (/var/run/docker.sock on Linux/macOS).
//
// The auto-detection covers the most common case where the daemon
// is running but `docker` CLI works only because the user's shell
// has a docker context configured — the SDK's FromEnv doesn't read
// those contexts, so without help it would fail with
// "/var/run/docker.sock: no such file" even though `docker ps` works.
//
// API-version negotiation is enabled by default in the SDK, so no
// explicit option needed.
//
// Compile-time assertion below ensures *mobyclient.Client satisfies
// our trimmed-down Client interface.
func NewClient(opts ...mobyclient.Opt) (*mobyclient.Client, error) {
	chosen := []mobyclient.Opt{mobyclient.FromEnv}

	if os.Getenv("DOCKER_HOST") == "" {
		if sock := autoDetectSocket(); sock != "" {
			chosen = append(chosen, mobyclient.WithHost("unix://"+sock))
		}
	}

	chosen = append(chosen, opts...)

	return mobyclient.New(chosen...)
}

// autoDetectSocket returns the first Docker socket path that exists
// from a list of well-known non-default locations. Empty string
// means "fall through to the SDK default".
func autoDetectSocket() string {
	candidates := []string{}

	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".colima", "default", "docker.sock"),
			filepath.Join(home, ".docker", "run", "docker.sock"),
		)
	}

	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		candidates = append(candidates, filepath.Join(xdg, "docker.sock"))
	}

	for _, p := range candidates {
		// gosec G304/G703: paths are constructed from os.UserHomeDir +
		// fixed suffixes or $XDG_RUNTIME_DIR + a fixed suffix. No
		// caller-supplied input flows in; treating a stat result as
		// "exists" carries no traversal risk.
		if _, err := os.Stat(p); err == nil { //nolint:gosec // candidates are HOME/XDG-anchored fixed suffixes, no user input
			return p
		}
	}

	return ""
}

// Compile-time check: the real moby client satisfies our subset.
// If the SDK adds or renames a method on our subset, this fails
// to build, which is what we want — better than a runtime surprise.
var _ Client = (*mobyclient.Client)(nil)
