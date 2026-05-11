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
// Matches the system executor's FHS-shaped agent root, except
// runtime + each BIN come from image mounts at /opt/* instead of
// being copied into /agent/usr/bin.
const (
	// inContainerAgentRoot hosts the agent's FHS-shaped tree
	// (etc/, home/, var/lib/, …). The host materialised root is
	// bind-mounted here. Hidden under /agent/ so the user-facing
	// CWD doesn't have a "workspace/workspace" elbow.
	inContainerAgentRoot = "/agent"

	// inContainerWorkspace is the agent's scratch dir — its CWD,
	// where files the agent creates land. The host's
	// `<agent-root>/workspace/` is *also* bind-mounted here so
	// writes show up at one stable, top-level path.
	inContainerWorkspace = "/workspace"

	// inContainerRuntimeDir hosts the runtime image-mount.
	inContainerRuntimeDir = "/opt/runtime"

	// inContainerBinsRoot hosts one image-mount per BIN at
	// /opt/bins/<name>/. Each BIN image's filesystem appears
	// inside its directory; the binary itself is whatever the
	// image lays down (typically `/`-rooted).
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

	AgentRoot     string // host path, bind-mounted at /agent (FHS root)
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
}

// buildConfig assembles the moby Container.Config for the agent's
// runtime container. The container's entrypoint is the runtime
// binary at the image-mount path; argv is `serve --root /agent
// --model <model> --addr 0.0.0.0:9999`.
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
		// WORKSPACE.md tells the model; the FHS tree lives under
		// /agent/ but the user-facing CWD stays a clean top-level
		// /workspace.
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
	mounts := []mounttypes.Mount{
		// FHS tree (etc/, usr/, home/, tmp/, var/, workspace/)
		// hidden at /agent. The runtime reads its config from
		// here via --root /agent.
		{
			Type:   mounttypes.TypeBind,
			Source: s.AgentRoot,
			Target: inContainerAgentRoot,
		},
		// Scratch dir bind-mounted at /workspace — the agent's
		// CWD. Same host data as /agent/workspace; surfacing it at
		// a clean top-level path is purely for the model's mental
		// model.
		{
			Type:   mounttypes.TypeBind,
			Source: filepath.Join(s.AgentRoot, "workspace"),
			Target: inContainerWorkspace,
		},
		{
			Type:     mounttypes.TypeImage,
			Source:   s.RuntimeImage,
			Target:   inContainerRuntimeDir,
			ReadOnly: true,
		},
	}

	for name, ref := range s.BINImages {
		mounts = append(mounts, mounttypes.Mount{
			Type:     mounttypes.TypeImage,
			Source:   ref,
			Target:   inContainerBinsRoot + "/" + name,
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
	binDirs := make([]string, 0, len(s.BINImages))
	for name := range s.BINImages {
		binDirs = append(binDirs, inContainerBinsRoot+"/"+name)
	}

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

	return env
}
