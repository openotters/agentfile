package docker

import (
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"

	containertypes "github.com/moby/moby/api/types/container"
	mounttypes "github.com/moby/moby/api/types/mount"
	networktypes "github.com/moby/moby/api/types/network"

	"github.com/openotters/agentfile/executor"
	"github.com/openotters/agentfile/spec"
)

// containerLayout describes the in-container paths the agent sees.
// The host materialised tree (etc/context, etc/data, etc/Agentfile,
// etc/agent.yaml, home, tmp, var/lib, workspace) is bind-mounted
// per-FHS-subdir directly onto the container's root filesystem. The
// agent's view of "/" IS its FHS root — no /agent prefix. Runtime
// and BIN images mount at /opt/* as before.
const (
	inContainerAgentRoot = "/" // passed as --root to the runtime
	inContainerWorkspace = "/workspace"

	// FHS bind-mount targets: each maps a piece of the host agent tree onto
	// the container's matching path.
	inContainerEtcContext = "/etc/context"
	inContainerEtcData    = "/etc/data"
	inContainerAgentfile  = "/etc/Agentfile"
	inContainerAgentYAML  = "/etc/agent.yaml"
	inContainerHome       = "/home"
	inContainerTmp        = "/tmp"
	inContainerVarLib     = "/var/lib"

	inContainerRuntimeDir = "/opt/runtime"    // runtime image-mount
	inContainerBinImages  = "/opt/bin-images" // per-BIN image mounts: /opt/bin-images/<name>/<name>
	inContainerBinsRoot   = "/opt/bins"       // flat PATH dir of symlinks into inContainerBinImages

	// inContainerDaemonSocket is where the daemon's unix socket is bind-mounted
	// (Linux mode), so OTTERSD_URL is stable regardless of the host path.
	inContainerDaemonSocket = "/run/ottersd.sock"

	agentGRPCPort = "9999" // in-container gRPC port, published to a random host loopback port

	labelOpenottersAgent = "io.openotters.agent" // marks executor-created containers for cleanup
	labelValueTrue       = "true"
)

// containerSpec is the resolved set of arguments needed to call
// ContainerCreate for an agent. Built once per Run from the
// materialised Runtime.
type containerSpec struct {
	Name      string
	BaseImage string

	// AgentRoot is the host path. agentFHSMounts maps each
	// top-level subtree onto its matching standard Linux path
	// inside the container — no /agent prefix.
	AgentRoot    string
	RuntimeImage string
	BINImages    map[string]string // name → ref
	UserMounts   []executor.Mount

	APIBase  string
	APIKey   string
	Provider string // "anthropic", "openai", … (for the env-var prefix)
	Model    string

	HostGRPCPort string // loopback port on host that maps to agentGRPCPort

	// DaemonURL is how the runtime reaches the host's daemon. Two
	// supported shapes:
	//
	//   unix:///<host-path>      bind-mount the daemon's unix socket
	//                            into the container at a canonical
	//                            in-container path; rewrite the URL
	//                            the agent dials accordingly. Clean
	//                            on native Linux Docker.
	//   http://<host>:<port>     dial the daemon's TCP listener over
	//                            host.docker.internal. Used on
	//                            Docker Desktop / Colima where unix-
	//                            socket bind-mount fails through
	//                            virtiofs.
	//
	// AgentToken is the JWT presented as Authorization: Bearer …
	// Empty DaemonURL = no OTTERSD_URL / OTTERS_AGENT_TOKEN env
	// injection.
	DaemonURL  string
	AgentToken string

	// UserEnvs are agentspec ENV declarations appended onto the
	// locked-down env after provider creds. spec.Validate rejects
	// reserved keys at build time; AppendUserEnv filters defensively
	// at runtime.
	UserEnvs []*spec.Env

	// Exec is the EXEC override; when empty the runtime is invoked with the
	// default `serve` verb.
	Exec []string

	// Configs is the resolved CONFIG map (kebab-case key → string), exported
	// on the spawn env as RUNTIME_<UPPER_SNAKE>.
	Configs map[string]string
}

// buildConfig assembles the Container.Config: the runtime binary as entrypoint
// with the EXEC verb (default serve) plus the appended --root/--model/--addr
// flags, spawned in /workspace.
func (s *containerSpec) buildConfig() *containertypes.Config {
	args := s.Exec
	if len(args) == 0 {
		args = []string{"serve"}
	}

	args = append(append([]string{}, args...),
		"--root", inContainerAgentRoot,
		"--model", s.Model,
		"--addr", "0.0.0.0:"+agentGRPCPort,
	)

	port, _ := networktypes.ParsePort(agentGRPCPort + "/tcp")

	return &containertypes.Config{
		Image:      s.BaseImage,
		Entrypoint: []string{inContainerRuntimeDir + "/runtime"},
		Cmd:        args,
		Env:        s.buildEnv(),
		// Spawn the runtime in /workspace — the bind-mounted scratch
		// dir — so tool subprocesses inherit that CWD. Matches what
		// WORKSPACE.md tells the model.
		WorkingDir: inContainerWorkspace,
		User:       "65532:65532", // distroless nonroot
		ExposedPorts: networktypes.PortSet{
			port: struct{}{},
		},
		Labels: map[string]string{
			labelOpenottersAgent: labelValueTrue,
		},
	}
}

// buildHostConfig assembles the moby Container.HostConfig: bind
// the agent root, image-mount runtime + BINs, publish the gRPC
// port, configure network access.
func (s *containerSpec) buildHostConfig() *containertypes.HostConfig {
	mounts := agentFHSMounts(s.AgentRoot)
	mounts = append(mounts, mounttypes.Mount{
		Type:     mounttypes.TypeImage,
		Source:   s.RuntimeImage,
		Target:   inContainerRuntimeDir,
		ReadOnly: true,
	})

	for name, ref := range s.BINImages {
		mounts = append(mounts, mounttypes.Mount{
			Type:     mounttypes.TypeImage,
			Source:   ref,
			Target:   inContainerBinImages + "/" + name,
			ReadOnly: true,
		})
	}

	for _, m := range s.UserMounts {
		mounts = append(mounts, mounttypes.Mount{
			Type:     mounttypes.TypeBind,
			Source:   m.Host,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}

	containerPort, _ := networktypes.ParsePort(agentGRPCPort + "/tcp")

	hostIP, _ := netip.ParseAddr("127.0.0.1")
	portBindings := networktypes.PortMap{
		containerPort: []networktypes.PortBinding{
			{HostIP: hostIP, HostPort: s.HostGRPCPort},
		},
	}

	hostCfg := &containertypes.HostConfig{
		Mounts:       mounts,
		PortBindings: portBindings,
		// Bridge networking is required for the published gRPC port to reach
		// the host loopback; outbound egress restriction is a deploy-policy
		// concern the daemon layers on.
		NetworkMode: containertypes.NetworkMode("bridge"),
		// Baseline hardening: drop all capabilities, forbid privilege
		// escalation, and cap process count against fork bombs. Memory/CPU
		// limits are left to the daemon's deploy policy.
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges"},
		Resources:   containertypes.Resources{PidsLimit: pidsLimit()},
	}

	// Wire daemon access only when the full callback pair is present, matching
	// the env injection's both-or-neither rule: a unix socket bind-mount, or a
	// host.docker.internal mapping for the TCP shape on plain Linux Docker.
	if s.DaemonURL != "" && s.AgentToken != "" {
		if _, mount, extraHost := daemonAccess(s.DaemonURL); mount != nil {
			hostCfg.Mounts = append(hostCfg.Mounts, *mount)
		} else if extraHost != "" {
			hostCfg.ExtraHosts = append(hostCfg.ExtraHosts, extraHost)
		}
	}

	return hostCfg
}

// daemonAccess decides how the agent inside the container reaches
// the openotters daemon, given the host-side DaemonURL. Returns:
//
//   - containerURL: the URL the runtime should dial from inside the
//     container (may differ from the input when the input is a unix
//     socket — we always expose the socket at the canonical
//     inContainerDaemonSocket path).
//   - mount: a bind-mount to add to HostConfig.Mounts when the
//     socket scheme is in use; nil otherwise.
//   - extraHost: a `host.docker.internal:host-gateway` entry to add
//     to HostConfig.ExtraHosts when the TCP scheme is in use on
//     plain Linux Docker; empty otherwise.
//
// Empty DaemonURL returns zero values across the board — that's the
// "agent has no daemon callback" case and the caller skips both
// wiring legs.
func daemonAccess(daemonURL string) (string, *mounttypes.Mount, string) {
	if daemonURL == "" {
		return "", nil, ""
	}

	const unixScheme = "unix://"

	if strings.HasPrefix(daemonURL, unixScheme) {
		hostPath := strings.TrimPrefix(daemonURL, unixScheme)

		return unixScheme + inContainerDaemonSocket,
			&mounttypes.Mount{
				Type:   mounttypes.TypeBind,
				Source: hostPath,
				Target: inContainerDaemonSocket,
			},
			""
	}

	if strings.Contains(daemonURL, "host.docker.internal") {
		return daemonURL, nil, "host.docker.internal:host-gateway"
	}

	return daemonURL, nil, ""
}

// buildEnv produces the locked-down env (PATH points at the
// in-container BIN dirs; HOME / XDG_* / TMPDIR live under /agent)
// plus per-provider credentials. AgentRoot is the FHS bind target
// (/agent), not the scratch CWD (/workspace) — HOME has to resolve
// to /agent/home, which is where the materialised tree's home/ dir
// actually exists inside the container.
func (s *containerSpec) buildEnv() []string {
	// Flat PATH — every BIN tool resolves through a symlink in
	// /opt/bins/<name>, surfaced via the agent root's bind mount.
	// `/opt/bins/<name>` IS the executable (a symlink to
	// /opt/bin-images/<name>/<name>), not a wrapper directory.
	binDirs := []string{inContainerBinsRoot}

	// Rewrite the daemon URL to the in-container form when we're
	// bind-mounting the socket: the agent always dials
	// inContainerDaemonSocket; the host-side path lives only on the
	// bind-mount entry, not in the env.
	daemonURL, _, _ := daemonAccess(s.DaemonURL)

	env := executor.BuildLockedEnv(executor.EnvOptions{
		AgentRoot:  inContainerAgentRoot,
		BinDirs:    binDirs,
		DaemonURL:  daemonURL,
		AgentToken: s.AgentToken,
	})

	if s.Provider != "" {
		prefix := strings.ToUpper(s.Provider)
		if s.APIKey != "" {
			env = append(env, fmt.Sprintf("%s_API_KEY=%s", prefix, s.APIKey))
		}
		if s.APIBase != "" {
			env = append(env, fmt.Sprintf("%s_API_BASE=%s", prefix, s.APIBase))
		}
	}

	// CONFIG first, ENV last: on a same-name collision (a deliberate
	// RUNTIME_*-prefixed ENV vs a CONFIG export) the explicit ENV wins
	// last-write.
	env = executor.AppendConfigEnv(env, s.Configs)
	env, _ = executor.AppendUserEnv(env, s.UserEnvs)

	return env
}

// agentFHSMounts builds the per-subdir bind-mount list that maps
// the host's materialised agent tree onto the container's FHS root.
// Each entry below corresponds to one top-level piece of the host
// agent layout being surfaced at its standard Linux path inside the
// container — no /agent prefix, no host path visible to the model.
// The four /etc entries are mounted individually rather than mounting
// the whole /etc dir so distroless's /etc/passwd / /etc/group /
// /etc/ssl/certs stay intact (nonroot user resolution + HTTPS).
// defaultPidsLimit caps the process count in an agent container to blunt fork
// bombs while leaving ample headroom for a normal tool workload.
const defaultPidsLimit = 512

func pidsLimit() *int64 {
	limit := int64(defaultPidsLimit)

	return &limit
}

func agentFHSMounts(agentRoot string) []mounttypes.Mount {
	bind := func(sub, target string, readOnly bool) mounttypes.Mount {
		return mounttypes.Mount{
			Type:     mounttypes.TypeBind,
			Source:   filepath.Join(agentRoot, sub),
			Target:   target,
			ReadOnly: readOnly,
		}
	}
	roBind := func(sub, target string) mounttypes.Mount { return bind(sub, target, true) }
	rwBind := func(sub, target string) mounttypes.Mount { return bind(sub, target, false) }

	return []mounttypes.Mount{
		// Config and context are read-only so a tool-using model cannot
		// rewrite its own agent.yaml / AGENT.md and persist it.
		roBind("etc/context", inContainerEtcContext),
		roBind("etc/data", inContainerEtcData),
		roBind("etc/Agentfile", inContainerAgentfile),
		roBind("etc/agent.yaml", inContainerAgentYAML),
		rwBind("home", inContainerHome),
		rwBind("tmp", inContainerTmp),
		rwBind("var/lib", inContainerVarLib),
		rwBind("workspace", inContainerWorkspace),
		// /opt/bins on the host carries per-BIN symlinks created by
		// materialise. Each symlink's target string points at
		// /opt/bin-images/<name>/<name> — a container-local path that
		// resolves at access time (the image mounts above land there).
		// Bind-mounting the host symlink dir keeps the model's view
		// flat: `/opt/bins/yaegi` IS the binary, not a wrapper dir.
		{
			Type:     mounttypes.TypeBind,
			Source:   filepath.Join(agentRoot, "opt", "bins"),
			Target:   inContainerBinsRoot,
			ReadOnly: true,
		},
	}
}
