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
	// inContainerAgentRoot is the agent's FHS root inside the
	// container — just "/". Passed as --root to the runtime.
	inContainerAgentRoot = "/"

	// inContainerWorkspace is the agent's scratch dir / CWD.
	inContainerWorkspace = "/workspace"

	// Per-FHS-subdir bind-mount targets. Each maps a top-level
	// piece of the host agent tree onto the container's matching
	// FHS path.
	inContainerEtcContext = "/etc/context"
	inContainerEtcData    = "/etc/data"
	inContainerAgentfile  = "/etc/Agentfile"
	inContainerAgentYAML  = "/etc/agent.yaml"
	inContainerHome       = "/home"
	inContainerTmp        = "/tmp"
	inContainerVarLib     = "/var/lib"

	// inContainerRuntimeDir hosts the runtime image-mount.
	inContainerRuntimeDir = "/opt/runtime"

	// inContainerBinImages hosts the per-BIN image mounts —
	// /opt/bin-images/<name>/<name>. Each BIN image's filesystem
	// (a single binary file at /<name>) lands in its own
	// subdirectory. Hidden from the model: PATH and tool
	// invocations go through /opt/bins/<name> symlinks below.
	inContainerBinImages = "/opt/bin-images"

	// inContainerBinsRoot is the flat directory the model sees in
	// PATH. Each entry is a symlink → /opt/bin-images/<name>/<name>.
	// Materialise creates the symlinks on the host side; a
	// bind-mount surfaces them inside the container so an `ls
	// /opt/bins` reads as a plain list of executables instead of
	// the doubled-up <name>/<name> path the raw image mounts
	// would produce.
	inContainerBinsRoot = "/opt/bins"

	// inContainerDaemonSocket is the canonical in-container path
	// where we bind-mount the daemon's unix socket (Linux mode).
	// Regardless of where the host socket lives, the agent inside
	// always dials this same path, so OTTERSD_URL is stable.
	inContainerDaemonSocket = "/run/ottersd.sock"

	// agentGRPCPort is the in-container port the runtime serves
	// gRPC on. Published to the host on a random loopback port.
	agentGRPCPort = "9999"

	// labelOpenottersAgent marks every container the docker
	// executor creates so cleanup / `docker ps --filter` calls
	// can find them without depending on the container name.
	labelOpenottersAgent = "io.openotters.agent"

	// labelValueTrue is the canonical "this label is set" value
	// — extracted to keep a stricter goconst lint rule happy
	// when "true" appears in multiple files.
	labelValueTrue = "true"
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
	AgentRoot     string
	RuntimeImage  string
	BINImages     map[string]string // name → ref
	UserMounts    []executor.Mount
	NetworkAccess bool

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

	// Configs is the resolved CONFIG map (kebab-case key → string).
	// Exported on the spawn env as RUNTIME_<UPPER_SNAKE> alongside
	// the agent.yaml `configs:` block, so subprocess wrappers can
	// read tunables without re-parsing the YAML.
	Configs map[string]string
}

// buildConfig assembles the moby Container.Config for the agent's
// runtime container. The container's entrypoint is the runtime
// binary at the image-mount path; argv is `serve --root /
// --model <model> --addr 0.0.0.0:9999` — the agent's FHS lives
// directly at the container's filesystem root.
func (s *containerSpec) buildConfig() *containertypes.Config {
	args := []string{
		"serve",
		"--root", inContainerAgentRoot,
		"--model", s.Model,
		"--addr", "0.0.0.0:" + agentGRPCPort,
	}

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

	networkMode := containertypes.NetworkMode("bridge")
	if !s.NetworkAccess {
		// We still need bridge networking for the published gRPC
		// port to work on the host loopback. Restricting outbound
		// access lands as a follow-up via iptables / network
		// policies; for now, network access is on by default for
		// agents.
		networkMode = containertypes.NetworkMode("bridge")
	}

	hostCfg := &containertypes.HostConfig{
		Mounts:       mounts,
		PortBindings: portBindings,
		NetworkMode:  networkMode,
	}

	// Wire daemon access. Two mutually exclusive shapes:
	//   - unix socket: bind-mount the host socket into the container.
	//   - TCP via host.docker.internal: add the ExtraHosts mapping on
	//     plain Linux Docker (Mac / Colima already provide the name).
	if _, mount, extraHost := daemonAccess(s.DaemonURL); mount != nil {
		hostCfg.Mounts = append(hostCfg.Mounts, *mount)
	} else if extraHost != "" {
		hostCfg.ExtraHosts = append(hostCfg.ExtraHosts, extraHost)
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

	env, _ = executor.AppendUserEnv(env, s.UserEnvs)
	env = executor.AppendConfigEnv(env, s.Configs)

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
func agentFHSMounts(agentRoot string) []mounttypes.Mount {
	return []mounttypes.Mount{
		{Type: mounttypes.TypeBind, Source: filepath.Join(agentRoot, "etc", "context"), Target: inContainerEtcContext},
		{Type: mounttypes.TypeBind, Source: filepath.Join(agentRoot, "etc", "data"), Target: inContainerEtcData},
		{Type: mounttypes.TypeBind, Source: filepath.Join(agentRoot, "etc", "Agentfile"), Target: inContainerAgentfile},
		{Type: mounttypes.TypeBind, Source: filepath.Join(agentRoot, "etc", "agent.yaml"), Target: inContainerAgentYAML},
		{Type: mounttypes.TypeBind, Source: filepath.Join(agentRoot, "home"), Target: inContainerHome},
		{Type: mounttypes.TypeBind, Source: filepath.Join(agentRoot, "tmp"), Target: inContainerTmp},
		{Type: mounttypes.TypeBind, Source: filepath.Join(agentRoot, "var", "lib"), Target: inContainerVarLib},
		{Type: mounttypes.TypeBind, Source: filepath.Join(agentRoot, "workspace"), Target: inContainerWorkspace},
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
