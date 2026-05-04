package system

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

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
	localRuntime  string
	logDir        string
	loopback      LoopbackAllocator
	agentDefaults []AgentOption
}

// NewProvider creates a system Provider. storeFor is called once per
// Create to produce the oras.ReadOnlyTarget backing that agent's image.
func NewProvider(root billy.Filesystem, storeFor StoreFor, opts ...ProviderOption) *Provider {
	a := &Provider{
		fs:        root,
		hostFS:    osfs.New("/"),
		storeFor:  storeFor,
		ociPuller: agentoci.RemotePuller(),
		loopback:  defaultLoopbackAllocator{},
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

// CreateWithOptions is Create plus a slice of per-instance AgentOption
// values (mounts, custom stdout, …). Kept as an extra method so the
// executor.Provider interface stays narrow — only the system provider
// understands these options; other providers keep working unchanged.
func (a *Provider) CreateWithOptions(
	_ context.Context, id uuid.UUID, ref spec.Reference,
	extra []AgentOption, overrides ...spec.Override,
) (executor.Agent, error) {
	chrootfs, err := a.fs.Chroot(id.String())
	if err != nil {
		return nil, fmt.Errorf("chrootfs: %w", err)
	}

	addr, err := a.loopback.Reserve()
	if err != nil {
		return nil, fmt.Errorf("reserving runtime address: %w", err)
	}

	agentOpts := append(a.agentOpts(ref, overrides), WithAddr(addr))

	if logFile, logErr := a.openLogFile(id); logErr == nil && logFile != nil {
		agentOpts = append(agentOpts, WithStdout(logFile), WithStderr(logFile))
	}

	agentOpts = append(agentOpts, extra...)

	return NewAgent(id, chrootfs, agentOpts...), nil
}

// openLogFile returns an append-mode writer at <logDir>/<id>.log or
// nil if logDir is unset. Callers treat nil as "use the default
// stdout/stderr from NewAgent". The file descriptor is intentionally
// leaked for the agent's lifetime — closing it on Stop would force us
// to reopen on Start, and subprocesses outlive individual gRPC calls.
//
// Reads/writes go through hostFS (non-chrooted), so the log directory
// lives on the real host disk in production and in-memory in tests.
func (a *Provider) openLogFile(id uuid.UUID) (io.Writer, error) {
	if a.logDir == "" {
		return nil, nil //nolint:nilnil // documented contract: nil writer = use default stdout/stderr
	}

	if err := a.hostFS.MkdirAll(a.logDir, 0o755); err != nil {
		return nil, err
	}

	return a.hostFS.OpenFile(
		filepath.Join(a.logDir, id.String()+".log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0o644,
	)
}

// Load recovers previously created agents from existing chroot directories.
func (a *Provider) Load(_ context.Context) ([]executor.Agent, error) {
	entries, err := a.fs.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("reading root: %w", err)
	}

	var agents []executor.Agent

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		id, parseErr := uuid.Parse(entry.Name())
		if parseErr != nil {
			continue
		}

		chrootfs, chrootErr := a.fs.Chroot(entry.Name())
		if chrootErr != nil {
			continue
		}

		rt, loadErr := executor.LoadRuntime(chrootfs)
		if loadErr != nil {
			continue
		}

		rt.ID = id

		// Loaded agents are already materialized -- apply defaults.
		ag := NewAgent(id, chrootfs, a.agentDefaults...)
		ag.markInitialized(rt)

		agents = append(agents, ag)
	}

	return agents, nil
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
