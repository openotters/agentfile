package docker

import (
	"github.com/openotters/agentfile/executor"
	"github.com/openotters/agentfile/model"
)

// ProviderOption configures the Docker Provider.
type ProviderOption func(*Provider)

// WithClient overrides the moby/moby client. Production callers omit
// this so the Provider builds one via NewClient (DOCKER_HOST + API
// negotiation from env). Tests pass a mock implementing the Client
// interface to drive lifecycle calls without a real daemon.
func WithClient(c Client) ProviderOption {
	return func(p *Provider) { p.client = c }
}

// WithBaseImage overrides the base image used to launch every agent
// container. Default is "gcr.io/distroless/static-debian12:nonroot".
// Pass "scratch" if you need a base with no /etc/passwd at all.
func WithBaseImage(ref string) ProviderOption {
	return func(p *Provider) { p.baseImage = ref }
}

// WithSkipVerify disables the daemon Verify() probe at NewProvider
// time. Tests use it to construct a Provider against a mock without
// the real Info/ServerVersion calls; production callers should not
// set it.
func WithSkipVerify() ProviderOption {
	return func(p *Provider) { p.skipVerify = true }
}

// WithModelResolver wires a model.Resolver onto the docker
// Provider. The Provider passes it to each Agent so credentials
// are looked up at materialise time.
func WithModelResolver(r model.Resolver) ProviderOption {
	return func(p *Provider) { p.modelResolver = r }
}

// WithLogDir captures each agent's runtime stdout/stderr to
// <dir>/<agent-id>.log. Same shape as the system executor's option;
// not yet wired through the docker Agent's container-logs flow but
// reserved for the follow-up that pipes ContainerLogs into a file.
func WithLogDir(dir string) ProviderOption {
	return func(p *Provider) { p.logDir = dir }
}

// WithMounts attaches user mounts (`-v`) to every agent the
// Provider creates. Same semantics as the system executor.
func WithMounts(m []executor.Mount) ProviderOption {
	return func(p *Provider) { p.mounts = m }
}

// AgentOption configures one Agent at Create time. Mirrors the
// system executor's per-agent option mechanism (see
// agentfile/executor/system/options.go); both packages expose
// "provider creates agents" + "callers may layer per-agent config"
// so the openotters daemon's pool.createAgent can thread daemon
// URL / agent token / log writers / etc. without polluting the
// abstract executor.Provider.Create signature.
type AgentOption func(*agentDeps)

// WithDaemonSocket sets the host filesystem path of the openotters
// daemon's unix socket. The docker backend bind-mounts it into the
// container at a fixed in-container path and exposes it to the
// runtime as OTTERSD_URL=unix://<in-container-path>. Empty disables
// both the mount and the env var.
func WithDaemonSocket(hostPath string) AgentOption {
	return func(d *agentDeps) { d.daemonSocket = hostPath }
}

// WithAgentToken sets the JWT minted by the daemon for this agent;
// injected into the container env as OTTERS_AGENT_TOKEN. Empty
// disables the env var (runtime spawns fine — outbound daemon
// calls just fail Unauthenticated).
func WithAgentToken(token string) AgentOption {
	return func(d *agentDeps) { d.agentToken = token }
}
