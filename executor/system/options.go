package system

import (
	"io"

	"github.com/go-git/go-billy/v6"
	"oras.land/oras-go/v2"

	"github.com/openotters/agentfile/executor"
	"github.com/openotters/agentfile/model"
	agentoci "github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
)

// ProviderOption configures the system Provider.
type ProviderOption func(*Provider)

// WithPuller sets the OCI puller for pulling runtime and tool binaries.
func WithPuller(p agentoci.Puller) ProviderOption {
	return func(a *Provider) { a.ociPuller = p }
}

// WithLocalRuntime overrides the runtime binary with a local path (skips OCI pull).
func WithLocalRuntime(path string) ProviderOption {
	return func(a *Provider) { a.localRuntime = path }
}

// WithAgentDefaults sets default AgentOptions applied to every created/loaded agent.
func WithAgentDefaults(opts ...AgentOption) ProviderOption {
	return func(a *Provider) { a.agentDefaults = opts }
}

// WithLogDir redirects each agent's runtime stdout/stderr to
// <dir>/<agent-id>.log (append mode). Set by consumers (like the
// openotters daemon) that want the subprocess output captured on disk
// rather than bleeding into their own stdout.
func WithLogDir(dir string) ProviderOption {
	return func(a *Provider) { a.logDir = dir }
}

// WithLoopbackAllocator overrides the default net.Listen-based
// allocator. Tests use it to hand out deterministic addresses without
// binding real ports. The default is defaultLoopbackAllocator, which
// binds 127.0.0.1:0 and closes the listener immediately.
func WithLoopbackAllocator(a LoopbackAllocator) ProviderOption {
	return func(p *Provider) { p.loopback = a }
}

// WithHostFS overrides the non-chrooted billy filesystem the Provider
// (and the agents it creates) use for real host paths — mount symlink
// targets, local-runtime source files, and the log directory. Default
// is osfs.New("/"); tests pass memfs.New() to keep everything in
// memory. Applied at Provider level so newly-created agents inherit
// the same hostFS automatically.
func WithHostFS(fs billy.Filesystem) ProviderOption {
	return func(p *Provider) { p.hostFS = fs }
}

// WithRegistryTarget supplies the oras.Target the system Registry
// façade reads from / writes to. The daemon plumbs in its embedded
// registry's target here. Tests can omit it; the Registry methods
// then return ErrNotImplemented.
func WithRegistryTarget(target oras.Target) ProviderOption {
	return func(p *Provider) { p.registryTarget = target }
}

// WithRegistryAddr supplies the embedded registry's HTTP address
// ("host:port") so the system Registry can do List / Inspect /
// Remove via the OCI distribution spec endpoints. Empty string
// means those methods return ErrNotImplemented.
func WithRegistryAddr(addr string) ProviderOption {
	return func(p *Provider) { p.registryAddr = addr }
}

// WithRegistryCreatedAt supplies a callback returning the
// unix-seconds when a manifest was first written to the embedded
// registry. The daemon's EmbeddedRegistry has this info via on-disk
// mtime. Optional — without it, ImageInfo.CreatedUnix is 0.
func WithRegistryCreatedAt(fn CreatedAtFunc) ProviderOption {
	return func(p *Provider) { p.registryCreatedAt = fn }
}

// withAgentHostFS is the AgentOption counterpart of WithHostFS, wired
// internally by Provider.agentOpts so every agent inherits the
// Provider's hostFS. Kept unexported because typical callers go
// through Provider; direct NewAgent users pass memfs/osfs themselves
// via WithHostFS at the Provider level.
func withAgentHostFS(fs billy.Filesystem) AgentOption {
	return func(a *Agent) { a.ws.hostFS = fs }
}

// AgentOption configures an individual agent.
type AgentOption func(*Agent)

// WithStore sets the OCI store for loading the agentfile.
func WithStore(s oras.ReadOnlyTarget) AgentOption {
	return func(a *Agent) { a.ws.store = s }
}

// WithReference sets the OCI reference for the agent image.
func WithReference(ref spec.Reference) AgentOption {
	return func(a *Agent) { a.ws.ref = ref }
}

// WithOverrides sets spec-level overrides (model, runtime, etc.).
func WithOverrides(overrides ...spec.Override) AgentOption {
	return func(a *Agent) { a.ws.overrides = overrides }
}

// WithAgentPuller sets the OCI puller on the agent.
func WithAgentPuller(p agentoci.Puller) AgentOption {
	return func(a *Agent) { a.ws.ociPuller = p }
}

// WithAgentLocalRuntime sets a local runtime binary path on the agent.
func WithAgentLocalRuntime(path string) AgentOption {
	return func(a *Agent) { a.ws.localRuntime = path }
}

// WithModelResolver sets the model resolver for API credential resolution.
func WithModelResolver(r model.Resolver) AgentOption {
	return func(a *Agent) { a.ws.modelResolver = r }
}

// WithStaticModelResolver sets a static API URL and key for model resolution.
func WithStaticModelResolver(apiURL, apiKey string) AgentOption {
	return func(a *Agent) { a.ws.modelResolver = model.StaticResolver(apiURL, apiKey) }
}

// DigestResolver returns the OCI digest of an image reference (or the
// empty string if the resolver can't answer). Used by workspace
// materialisation to record provenance into agent.yaml.
type DigestResolver func(ref string) string

// WithDigestResolver wires a digest resolver into the workspace. The
// resolver is consulted for the agent's own image, the RUNTIME image,
// and every BIN tool, with the results written into the resolved
// agent.yaml's provenance block + per-tool ref/digest fields.
func WithDigestResolver(r DigestResolver) AgentOption {
	return func(a *Agent) { a.ws.digestResolver = r }
}

// WithImageRef tells the workspace which image ref produced this
// agent. Without it, agent.yaml's provenance.image_digest stays empty
// because the workspace materialiser otherwise has no canonical
// "this is the agent image" handle — the daemon does.
func WithImageRef(ref string) AgentOption {
	return func(a *Agent) { a.ws.imageRef = ref }
}

// WithStdout sets the writer for agent stdout.
func WithStdout(w io.Writer) AgentOption {
	return func(a *Agent) { a.proc.stdout = w }
}

// WithStderr sets the writer for agent stderr.
func WithStderr(w io.Writer) AgentOption {
	return func(a *Agent) { a.proc.stderr = w }
}

// WithAddr sets the gRPC listen address for the agent.
func WithAddr(addr string) AgentOption {
	return func(a *Agent) { a.addr = addr }
}

// WithDaemonURL sets the openotters daemon URL the runtime should
// dial back to. Format is backend-conventional: for the system
// executor, callers typically pass `unix://<host-path>` (the
// chrooted runtime can dial host paths directly since the chroot is
// billy-rooted, not a real syscall chroot). Empty disables the
// env var.
func WithDaemonURL(url string) AgentOption {
	return func(a *Agent) { a.daemonURL = url }
}

// WithAgentToken sets the JWT minted by the daemon for this agent;
// injected into the spawn env as OTTERS_AGENT_TOKEN. The runtime
// presents it as `Authorization: Bearer …` on every outbound RPC.
// Empty disables the env var (runtime can still spawn — outbound
// daemon calls would fail Unauthenticated, which is the desired
// behaviour when the agent isn't supposed to call back).
func WithAgentToken(token string) AgentOption {
	return func(a *Agent) { a.agentToken = token }
}

// WithMounts attaches bind-mount specs to the agent. The symlinks
// are created by workspace.applyMounts at the end of materialize
// (or via Agent.ReapplyMounts on restore).
func WithMounts(m []executor.Mount) AgentOption {
	return func(a *Agent) { a.ws.mounts = m }
}

// WithSpawner overrides the process spawner used to launch the
// runtime binary. Production uses defaultSpawner (real os/exec);
// tests inject a mock so process.serve can be exercised end-to-end
// without spawning anything real.
func WithSpawner(s Spawner) AgentOption {
	return func(a *Agent) { a.proc.spawner = s }
}

// WithDialer overrides the gRPC dialer used to reach the runtime
// subprocess. Production uses defaultDialer (grpc.NewClient +
// WaitForStateChange until Ready); tests inject a bufconn-backed
// dialer so Prompt / PromptStream / PromptObject /
// ListSessionMessages can be exercised without a real subprocess.
func WithDialer(d Dialer) AgentOption {
	return func(a *Agent) { a.dialer = d }
}
