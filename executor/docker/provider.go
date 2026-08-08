package docker

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-billy/v6/util"
	"github.com/google/uuid"
	mobyclient "github.com/moby/moby/client"
	"oras.land/oras-go/v2"

	"github.com/openotters/agentfile/executor"
	"github.com/openotters/agentfile/model"
	agentoci "github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
)

// DefaultBaseImage is the container image agents launch from when no
// override is supplied. distroless/static is libc-free, has a non-root
// user (UID 65532), and weighs ~2 MB. Static Go binaries (the runtime
// + every BIN) don't need libc, so this is enough.
const DefaultBaseImage = "gcr.io/distroless/static-debian12:nonroot"

// StoreFor returns an OCI target backing a specific agent's image
// ref. Same shape as system.StoreFor — the daemon constructs both.
type StoreFor func(ref spec.Reference) oras.ReadOnlyTarget

// Provider implements executor.Provider against a Docker Engine via
// the moby/moby Go SDK.
type Provider struct {
	client    Client
	baseImage string
	registry  *registry

	// content-materialisation deps (mirror system.Provider's)
	root          billy.Filesystem // dir holding per-agent materialised content
	hostFS        billy.Filesystem
	storeFor      StoreFor
	ociPuller     agentoci.Puller
	usageFetcher  agentoci.UsageFetcher
	modelResolver model.Resolver
	mounts        []executor.Mount
	logDir        string

	// skipVerify suppresses the daemon-readiness probe at
	// NewProvider time. Tests set it; production never does.
	skipVerify bool
}

// NewProvider constructs a Docker provider. Pass WithClient to
// inject a mock in tests; production callers pass nothing and the
// Provider builds a default Client via NewClient (which honours
// DOCKER_HOST and negotiates the API version with the daemon).
//
// root is the host directory that holds per-agent materialised
// content (one subdir per agent UUID). storeFor is consulted at
// Create time to load the agent OCI artifact. Both have the same
// shape as the system executor's equivalents.
func NewProvider(root billy.Filesystem, storeFor StoreFor, opts ...ProviderOption) (*Provider, error) {
	p := &Provider{
		baseImage: DefaultBaseImage,
		root:      root,
		hostFS:    osfs.New("/"),
		storeFor:  storeFor,
		// Binaries run inside Linux containers regardless of the
		// host OS, so multi-arch refs resolve against linux/<arch>.
		ociPuller:    agentoci.RemotePuller(agentoci.LinuxPlatform()),
		usageFetcher: agentoci.RemoteUsageFetcher(agentoci.LinuxPlatform()),
	}

	for _, opt := range opts {
		opt(p)
	}

	if p.client == nil {
		cli, err := NewClient()
		if err != nil {
			return nil, fmt.Errorf("docker: build default client: %w", err)
		}

		p.client = cli
	}

	if !p.skipVerify {
		if err := Verify(context.Background(), p.client); err != nil {
			return nil, err
		}
	}

	return p, nil
}

// Create returns a new Agent bound to the provided ID + image
// reference. The agent is not yet started; call Run / Start.
func (p *Provider) Create(
	ctx context.Context, id uuid.UUID, ref spec.Reference, overrides ...spec.Override,
) (executor.Agent, error) {
	return p.CreateWithOptions(ctx, id, ref, nil, overrides...)
}

// CreateWithOptions is the per-agent-options variant of Create. The
// openotters daemon's pool.createAgent uses it to thread daemon URL
// / agent token / future per-agent injections without changing the
// abstract executor.Provider.Create signature. Pure additive — Create
// is a thin wrapper that calls this with no AgentOption.
func (p *Provider) CreateWithOptions(
	_ context.Context, id uuid.UUID, ref spec.Reference,
	agentOpts []AgentOption, overrides ...spec.Override,
) (executor.Agent, error) {
	chrootfs, err := p.root.Chroot(id.String())
	if err != nil {
		return nil, fmt.Errorf("docker: chroot: %w", err)
	}

	hostPort, err := reserveLoopbackPort()
	if err != nil {
		return nil, fmt.Errorf("docker: reserve port: %w", err)
	}

	deps := agentDeps{
		id:           id,
		client:       p.client,
		baseImage:    p.baseImage,
		ref:          ref,
		overrides:    overrides,
		fs:           chrootfs,
		hostFS:       p.hostFS,
		store:        p.storeFor(ref),
		puller:       p.ociPuller,
		usageFetcher: p.usageFetcher,
		modelResolve: p.modelResolver,
		mounts:       p.mounts,
		hostGRPCPort: hostPort,
		logDir:       p.logDir,
	}
	for _, opt := range agentOpts {
		opt(&deps)
	}

	return newAgent(deps), nil
}

// Load lists existing agent directories on disk. Currently a stub —
// re-binding running containers to in-process Agent values is a
// follow-up; on daemon restart with the docker executor, agents
// are dropped from the pool and the user re-runs them.
func (p *Provider) Load(_ context.Context) ([]executor.Agent, error) {
	return nil, nil
}

// Destroy force-removes every container labelled as an openotters agent and
// deletes all per-agent directories, so a daemon that restarted (and could not
// re-adopt running containers, since Load is a stub) leaves nothing behind.
func (p *Provider) Destroy(ctx context.Context) error {
	if err := p.removeLabelledContainers(ctx); err != nil {
		return err
	}

	entries, err := p.root.ReadDir(".")
	if err != nil {
		return fmt.Errorf("docker: reading root: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		if _, parseErr := uuid.Parse(entry.Name()); parseErr != nil {
			continue
		}

		if rmErr := util.RemoveAll(p.root, entry.Name()); rmErr != nil {
			return fmt.Errorf("docker: removing %s: %w", entry.Name(), rmErr)
		}
	}

	return nil
}

// removeLabelledContainers force-removes every container carrying the agent
// label, running or stopped.
func (p *Provider) removeLabelledContainers(ctx context.Context) error {
	list, err := p.client.ContainerList(ctx, mobyclient.ContainerListOptions{
		All:     true,
		Filters: mobyclient.Filters{}.Add("label", labelOpenottersAgent+"="+labelValueTrue),
	})
	if err != nil {
		return fmt.Errorf("docker: listing agent containers: %w", err)
	}

	for _, c := range list.Items {
		if _, rmErr := p.client.ContainerRemove(ctx, c.ID, mobyclient.ContainerRemoveOptions{Force: true}); rmErr != nil &&
			!isNotFoundErr(rmErr) {
			return fmt.Errorf("docker: removing container %s: %w", c.ID, rmErr)
		}
	}

	return nil
}

// Close releases the underlying SDK client.
func (p *Provider) Close() error {
	return p.client.Close()
}

// Registry returns the Docker-backed executor.Registry façade.
func (p *Provider) Registry() executor.Registry {
	if p.registry == nil {
		p.registry = newRegistry(p.client)
	}

	return p.registry
}

// reserveLoopbackPort binds 127.0.0.1:0, releases it, and returns
// the port that was given out. There's a tiny TOCTOU window where
// another process could grab the same port before Docker's port
// binding takes effect; matches the system executor's identical
// pattern (executor/system/loopback.go).
func reserveLoopbackPort() (string, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}

	defer func() { _ = l.Close() }()

	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return "", fmt.Errorf("docker: listener returned non-TCP addr %T", l.Addr())
	}

	return fmt.Sprintf("%d", addr.Port), nil
}

// agentRootPath returns the host filesystem path of the agent's
// materialised content. Used by the docker executor to build the
// bind-mount source.
//

func agentRootPath(root billy.Filesystem, id uuid.UUID) string {
	return filepath.Join(root.Root(), id.String())
}

// openLogFile mirrors system.Provider.openLogFile so docker agents
// also persist runtime logs to disk when WithLogDir is configured.
//

func (p *Provider) openLogFile(id uuid.UUID) (*os.File, error) {
	if p.logDir == "" {
		return nil, nil //nolint:nilnil // documented "no log file" sentinel
	}

	if err := p.hostFS.MkdirAll(p.logDir, 0o755); err != nil {
		return nil, err
	}

	return os.OpenFile(
		filepath.Join(p.logDir, id.String()+".log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0o644,
	)
}

// Compile-time check that *Provider satisfies executor.Provider.
var _ executor.Provider = (*Provider)(nil)
