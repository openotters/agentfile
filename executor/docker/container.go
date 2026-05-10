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
			Type:   mounttypes.TypeBind,
			Source: m.Host,
			Target: m.Target,
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

	return &containertypes.HostConfig{
		Mounts:       mounts,
		PortBindings: portBindings,
		NetworkMode:  networkMode,
	}
}

// buildEnv produces the locked-down env (PATH points at the
// in-container BIN dirs; HOME / XDG_* / TMPDIR live under
// /workspace) plus per-provider credentials.
func (s *containerSpec) buildEnv() []string {
	binDirs := make([]string, 0, len(s.BINImages))
	for name := range s.BINImages {
		binDirs = append(binDirs, inContainerBinsRoot+"/"+name)
	}

	env := executor.BuildLockedEnv(executor.EnvOptions{
		AgentRoot: inContainerWorkspace,
		BinDirs:   binDirs,
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
