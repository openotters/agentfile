package system

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-billy/v6/util"
	"github.com/google/uuid"
	"oras.land/oras-go/v2"

	"github.com/openotters/agentfile/executor"
	agentoci "github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
)

// StoreFor returns an OCI target backing a specific agent's ref.
// The Provider invokes it once per Create, so each agent gets a target
// scoped to its own image — typically a remote.Repository bound to the
// agent's repo in the caller's local registry.
type StoreFor func(ref spec.Reference) oras.ReadOnlyTarget

// Provider implements executor.Provider using the local filesystem.
// It prepares the infrastructure (chroot dirs) but does not materialize agents.
type Provider struct {
	fs            billy.Filesystem
	hostFS        billy.Filesystem
	storeFor      StoreFor
	ociPuller     agentoci.Puller
	usageFetcher  agentoci.UsageFetcher
	localRuntime  string
	logDir        string
	loopback      LoopbackAllocator
	agentDefaults []AgentOption

	// The Registry façade is built lazily on first Registry() call.
	registryOnce      sync.Once
	registry          *registry
	registryTarget    oras.Target
	registryAddr      string
	registryCreatedAt CreatedAtFunc
}

// NewProvider creates a system Provider. storeFor is called once per
// Create to produce the oras.ReadOnlyTarget backing that agent's image.
func NewProvider(root billy.Filesystem, storeFor StoreFor, opts ...ProviderOption) *Provider {
	a := &Provider{
		fs:       root,
		hostFS:   osfs.New("/"),
		storeFor: storeFor,
		// Binaries run directly on the host, so multi-arch refs
		// resolve against the host platform.
		ociPuller:    agentoci.RemotePuller(agentoci.HostPlatform()),
		usageFetcher: agentoci.RemoteUsageFetcher(agentoci.HostPlatform()),
		loopback:     defaultLoopbackAllocator{},
	}

	for _, opt := range opts {
		opt(a)
	}

	return a
}

func (a *Provider) agentOpts(ref spec.Reference, overrides []spec.Override) []AgentOption {
	opts := []AgentOption{
		WithStore(a.storeFor(ref)),
		WithReference(ref),
		WithAgentPuller(a.ociPuller),
		WithAgentUsageFetcher(a.usageFetcher),
		withAgentHostFS(a.hostFS),
	}

	if a.localRuntime != "" {
		opts = append(opts, WithAgentLocalRuntime(a.localRuntime))
	}

	if len(overrides) > 0 {
		opts = append(opts, WithOverrides(overrides...))
	}

	// Provider defaults, then per-call overrides can be added by the caller.
	opts = append(opts, a.agentDefaults...)

	return opts
}

// Create prepares a chroot directory, reserves a loopback port for the
// runtime's gRPC server, and returns an Agent bound to both. The agent
// materializes itself on first Run.
func (a *Provider) Create(
	ctx context.Context, id uuid.UUID, ref spec.Reference, opts ...spec.Override,
) (executor.Agent, error) {
	return a.CreateWithOptions(ctx, id, ref, nil, opts...)
}

// CreateWithOptions is Create plus per-instance AgentOptions (mounts, custom
// stdout, …). Kept separate so executor.Provider stays narrow.
func (a *Provider) CreateWithOptions(
	_ context.Context, id uuid.UUID, ref spec.Reference,
	extra []AgentOption, overrides ...spec.Override,
) (executor.Agent, error) {
	chrootfs, err := a.fs.Chroot(id.String())
	if err != nil {
		return nil, fmt.Errorf("chrootfs: %w", err)
	}

	opts, err := a.instanceOptions(id, a.agentOpts(ref, overrides))
	if err != nil {
		return nil, err
	}

	return NewAgent(id, chrootfs, append(opts, extra...)...), nil
}

// instanceOptions appends the per-instance options every agent needs: a fresh
// loopback address and, when a log dir is configured, the log file wired as
// stdout/stderr and handed over for close-on-Remove.
func (a *Provider) instanceOptions(id uuid.UUID, base []AgentOption) ([]AgentOption, error) {
	addr, err := a.loopback.Reserve()
	if err != nil {
		return nil, fmt.Errorf("reserving runtime address: %w", err)
	}

	opts := make([]AgentOption, 0, len(base)+4)
	opts = append(opts, base...)
	opts = append(opts, WithAddr(addr))

	logFile, err := a.openLogFile(id)
	if err != nil {
		return nil, fmt.Errorf("opening log file: %w", err)
	}

	if logFile != nil {
		opts = append(opts, WithStdout(logFile), WithStderr(logFile), withLogCloser(logFile))
	}

	return opts, nil
}

// openLogFile opens <logDir>/<id>.log in append mode, or returns nil when no
// log dir is configured (the agent then keeps its default stdout/stderr). The
// caller closes it on Remove.
func (a *Provider) openLogFile(id uuid.UUID) (io.WriteCloser, error) {
	if a.logDir == "" {
		return nil, nil //nolint:nilnil // nil closer means "use default stdout/stderr"
	}

	if err := a.hostFS.MkdirAll(a.logDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating log dir: %w", err)
	}

	f, err := a.hostFS.OpenFile(
		filepath.Join(a.logDir, id.String()+".log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644,
	)
	if err != nil {
		return nil, fmt.Errorf("opening log file: %w", err)
	}

	return f, nil
}

// Load recovers previously created agents from their chroot directories. Each
// recovered agent gets the same instance options a freshly-created one would
// (a fresh address, log file, hostFS), and is marked already-materialised so it
// is not rebuilt. Per-agent load errors are collected and returned alongside
// the agents that did load, so one corrupt tree does not hide the rest.
func (a *Provider) Load(_ context.Context) ([]executor.Agent, error) {
	entries, err := a.fs.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("reading root: %w", err)
	}

	var (
		agents  []executor.Agent
		loadErr error
	)

	for _, entry := range entries {
		id, idErr := agentDirID(entry)
		if idErr != nil {
			continue
		}

		agent, agentErr := a.loadAgent(id)
		if agentErr != nil {
			loadErr = errors.Join(loadErr, fmt.Errorf("loading agent %s: %w", id, agentErr))

			continue
		}

		agents = append(agents, agent)
	}

	return agents, loadErr
}

// dirEntry is the intersection of os.FileInfo and fs.DirEntry that agentDirID
// needs, so it works regardless of which billy ReadDir returns.
type dirEntry interface {
	IsDir() bool
	Name() string
}

// agentDirID returns the agent UUID a directory entry names, or an error if it
// is not an agent directory.
func agentDirID(entry dirEntry) (uuid.UUID, error) {
	if entry == nil || !entry.IsDir() {
		return uuid.Nil, fmt.Errorf("not a directory")
	}

	return uuid.Parse(entry.Name())
}

func (a *Provider) loadAgent(id uuid.UUID) (executor.Agent, error) {
	chrootfs, err := a.fs.Chroot(id.String())
	if err != nil {
		return nil, fmt.Errorf("chroot: %w", err)
	}

	rt, err := executor.LoadRuntime(chrootfs)
	if err != nil {
		return nil, fmt.Errorf("reading agent.yaml: %w", err)
	}

	rt.ID = id

	ref := spec.Reference{}
	if rt.Image != nil {
		ref = spec.ParseReference(rt.Image.Ref)
	}

	opts, err := a.instanceOptions(id, a.agentOpts(ref, nil))
	if err != nil {
		return nil, err
	}

	ag := NewAgent(id, chrootfs, opts...)
	ag.markInitialized(rt)

	return ag, nil
}

// Destroy removes all agent chroot directories.
func (a *Provider) Destroy(_ context.Context) error {
	entries, err := a.fs.ReadDir(".")
	if err != nil {
		return fmt.Errorf("reading root: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		if _, parseErr := uuid.Parse(entry.Name()); parseErr != nil {
			continue
		}

		if removeErr := util.RemoveAll(a.fs, entry.Name()); removeErr != nil {
			return fmt.Errorf("removing %s: %w", entry.Name(), removeErr)
		}
	}

	return nil
}

// Registry returns the executor.Registry façade, built once on first call.
func (a *Provider) Registry() executor.Registry {
	a.registryOnce.Do(func() {
		a.registry = newRegistry(a.registryTarget, a.registryAddr, a.registryCreatedAt)
	})

	return a.registry
}

// Compile-time check that *Provider satisfies executor.Provider.
var _ executor.Provider = (*Provider)(nil)
