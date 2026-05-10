// Async-job Exec for the docker backend. Spawns a per-job container
// from the agent's base image with the SAME mount + env layout the
// runtime container uses (workspace bind-mounted at /workspace, BIN
// images mounted at /opt/bins/<name>/, locked-down env). The job
// container is labelled so the daemon's boot path can sweep ghosts
// from a previous incarnation that was killed -9 mid-flight.
//
// stdin is NOT supported in v1: the trimmed Client interface doesn't
// include ContainerAttach (the only way to pipe stdin into a fresh
// container). Stdin requests fail fast with a clear error so the
// caller can degrade. Adding stdin = follow-up that grows the
// Client interface and uses HijackedResponse for stdio multiplexing.
package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	containertypes "github.com/moby/moby/api/types/container"
	mounttypes "github.com/moby/moby/api/types/mount"
	mobyclient "github.com/moby/moby/client"

	"github.com/openotters/agentfile/executor"
)

// dockerStdCopy demultiplexes Docker's multiplexed log stream into
// separate stdout / stderr writers. The wire format (see the docker
// engine API "attach" + "logs" docs) is:
//
//	[8-byte header][payload]
//
// where header[0] is the stream id (0=stdin, 1=stdout, 2=stderr) and
// header[4:8] is a big-endian uint32 payload length. We inline this
// rather than depend on github.com/moby/moby/pkg/stdcopy because
// the legacy `moby/moby` module conflicts with the modular
// `moby/moby/api` + `moby/moby/client` modules already in use.
func dockerStdCopy(stdout, stderr io.Writer, src io.Reader) error {
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(src, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		size := int(binary.BigEndian.Uint32(hdr[4:8]))
		if size == 0 {
			continue
		}
		var dst io.Writer
		switch hdr[0] {
		case 1:
			dst = stdout
		case 2:
			dst = stderr
		default:
			// Stream id 0 (stdin) shouldn't appear on outbound logs;
			// other ids would be a protocol surprise. Discard the
			// payload to stay in sync with the framing.
			dst = io.Discard
		}
		if _, err := io.CopyN(dst, src, int64(size)); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
	}
}

// labelOpenottersAsyncJob marks every per-job container the docker
// async-exec path creates. The daemon's boot path can `docker ps
// --filter label=…` to find and remove ghost containers from a
// previous incarnation that was kill -9'd mid-job.
const labelOpenottersAsyncJob = "io.openotters.async-job-id"

// Exec runs `bin args...` in a new container spawned from the agent's
// base image, with the agent's workspace bind-mounted and every
// declared BIN image-mounted at /opt/bins/<name>. Cancellation kills
// the container; its log stream is captured and demuxed into
// stdout/stderr.
//
// The agent MUST be initialized (Prepare/Run has resolved its
// runtime + BINs) before Exec is called — otherwise we don't know
// what to mount. Calling Exec on an un-initialized agent surfaces
// in ExecResult.Err.
func (a *Agent) Exec(ctx context.Context, bin string, args []string, stdin string) executor.ExecResult {
	if stdin != "" {
		return executor.ExecResult{
			Err: errors.New(
				"docker async-exec: stdin is not supported in v1; " +
					"pass payload via --arg or write it to the workspace and read from there"),
		}
	}

	rt := a.Runtime()
	if rt == nil {
		return executor.ExecResult{
			Err: fmt.Errorf("docker async-exec: agent %s not initialized — call Prepare/Run first", a.deps.id),
		}
	}

	if a.deps.fs == nil {
		return executor.ExecResult{Err: fmt.Errorf("docker async-exec: agent %s has no filesystem", a.deps.id)}
	}

	// Fail fast when the requested BIN isn't in the agent's declared
	// tool set. Without this check the container spawns successfully,
	// runc fails with `exec: "<bin>": executable file not found`,
	// and the operator chases a misleading symptom. The declared BIN
	// list IS the source of truth — what's on PATH inside the
	// container is whatever the BIN image-mounts publish.
	if !binDeclared(rt, bin) {
		return executor.ExecResult{
			Err: fmt.Errorf(
				"docker async-exec: BIN %q is not declared in agent %s — add `BIN %s <ref>` to its Agentfile and rebuild, or pick one of: %s",
				bin, a.deps.id, bin, declaredBinNames(rt),
			),
		}
	}

	// Build the per-job container spec — same image-mount + env layout
	// the long-running runtime container uses, but the Cmd is the BIN
	// invocation (not the runtime entrypoint) and the labels carry
	// the job ID for ghost cleanup.
	jobLabel := "exec-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	cfg, hostCfg, err := a.buildExecContainer(rt, bin, args, jobLabel)
	if err != nil {
		return executor.ExecResult{Err: fmt.Errorf("docker async-exec: build container spec: %w", err)}
	}

	createResp, err := a.deps.client.ContainerCreate(ctx, mobyclient.ContainerCreateOptions{
		Config:     cfg,
		HostConfig: hostCfg,
		// No name — let docker assign one. We track via container ID.
	})
	if err != nil {
		return executor.ExecResult{Err: fmt.Errorf("docker async-exec: create: %w", err)}
	}

	id := createResp.ID
	handle := id

	// Always remove on exit, even if start / wait errors out.
	defer func() {
		_, _ = a.deps.client.ContainerRemove(context.Background(), id,
			mobyclient.ContainerRemoveOptions{Force: true})
	}()

	if _, err := a.deps.client.ContainerStart(ctx, id, mobyclient.ContainerStartOptions{}); err != nil {
		return executor.ExecResult{
			Err:    fmt.Errorf("docker async-exec: start %s: %w", id, err),
			Handle: handle,
		}
	}

	// Cancellation watcher: when the parent ctx is cancelled, send
	// SIGKILL to the container so the local Wait/Logs loop unblocks.
	// Done in a separate goroutine because ContainerStop blocks on
	// the API round-trip; we don't want to slow down the happy path.
	killCtx, killCancel := context.WithCancel(context.Background())
	defer killCancel()
	go func() {
		select {
		case <-ctx.Done():
			// 0s grace: jobs are SIGKILL'd because graceful TERM may
			// not propagate (some BINs ignore SIGTERM in pipelines).
			zero := 0
			_, _ = a.deps.client.ContainerStop(context.Background(), id,
				mobyclient.ContainerStopOptions{Timeout: &zero})
		case <-killCtx.Done():
		}
	}()

	// Stream logs with Follow=true. The stream EOFs when the container
	// exits — that's our wait signal. Without the SDK's ContainerWait
	// in our trimmed Client interface, this is the lightest path.
	logsResp, err := a.deps.client.ContainerLogs(ctx, id, mobyclient.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
	})
	if err != nil {
		return executor.ExecResult{
			Err:    fmt.Errorf("docker async-exec: logs %s: %w", id, err),
			Handle: handle,
		}
	}
	defer logsResp.Close()

	var stdout, stderr bytes.Buffer
	if err := dockerStdCopy(&stdout, &stderr, logsResp); err != nil &&
		!errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
		// Non-EOF errors usually mean the docker daemon went away.
		// Capture what we have and surface the underlying error.
		return executor.ExecResult{
			Stdout: stdout.String(),
			Stderr: stderr.String(),
			Err:    fmt.Errorf("docker async-exec: stream logs: %w", err),
			Handle: handle,
		}
	}

	// Inspect for the exit code. Use a fresh ctx — the parent ctx may
	// already be cancelled if we got here via the killer goroutine.
	insp, err := a.deps.client.ContainerInspect(context.Background(), id,
		mobyclient.ContainerInspectOptions{})
	if err != nil {
		return executor.ExecResult{
			Stdout: stdout.String(),
			Stderr: stderr.String(),
			Err:    fmt.Errorf("docker async-exec: inspect %s: %w", id, err),
			Handle: handle,
		}
	}

	exit := 0
	if insp.Container.State != nil {
		exit = int(insp.Container.State.ExitCode)
	}

	out := executor.ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exit,
		Handle:   handle,
	}
	// If the parent ctx was cancelled mid-run, surface that on Err so
	// callers can distinguish "BIN ran and exited" from "we killed it."
	if ctxErr := ctx.Err(); ctxErr != nil {
		out.Err = ctxErr
	}
	return out
}

// buildExecContainer assembles the Container.Config + HostConfig for
// one async-job container. Mirrors containerSpec's mount layout
// (workspace at /workspace, agent root at /agent, runtime image at
// /opt/runtime, each BIN at /opt/bins/<name>) so the job sees the
// identical filesystem the agent's runtime sees.
func (a *Agent) buildExecContainer(
	rt *executor.Runtime, bin string, args []string, jobLabel string,
) (*containertypes.Config, *containertypes.HostConfig, error) {
	bins := make(map[string]string, len(rt.Tools))
	for _, t := range rt.Tools {
		bins[t.Name] = t.Ref
	}

	binDirs := make([]string, 0, len(bins))
	for name := range bins {
		binDirs = append(binDirs, inContainerBinsRoot+"/"+name)
	}

	var daemonURL string
	if a.deps.daemonSocket != "" {
		daemonURL = "unix://" + inContainerDaemonSocket
	}
	env := executor.BuildLockedEnv(executor.EnvOptions{
		AgentRoot:  inContainerAgentRoot,
		BinDirs:    binDirs,
		DaemonURL:  daemonURL,
		AgentToken: a.deps.agentToken,
	})

	// Provider creds live with the agent; jobs MAY need them too
	// (e.g. a job that calls another LLM via the same provider).
	// Same shape as containerSpec.buildEnv().
	provider, _ := splitProviderPrefix(rt.Model)
	if provider != "" {
		prefix := strings.ToUpper(provider)
		if rt.APIKey != "" {
			env = append(env, fmt.Sprintf("%s_API_KEY=%s", prefix, rt.APIKey))
		}
		if rt.APIBase != "" {
			env = append(env, fmt.Sprintf("%s_API_BASE=%s", prefix, rt.APIBase))
		}
	}
	if rt.Source != nil && rt.Source.Agent != nil {
		env, _ = executor.AppendUserEnv(env, rt.Source.Agent.Envs)
	}

	cmd := append([]string{bin}, args...)

	cfg := &containertypes.Config{
		Image:      a.deps.baseImage,
		Cmd:        cmd,
		Env:        env,
		WorkingDir: inContainerWorkspace,
		User:       "65532:65532", // distroless nonroot — matches the agent's runtime container
		Labels: map[string]string{
			labelOpenottersAgent:    labelValueTrue,
			labelOpenottersAsyncJob: jobLabel,
			"io.openotters.agent-id": a.deps.id.String(),
		},
		// Don't open stdin — v1 doesn't support stdin (see the function
		// docstring). Setting this true would block ContainerStart on
		// us never attaching.
		OpenStdin:    false,
		AttachStdout: false,
		AttachStderr: false,
	}

	mounts := []mounttypes.Mount{
		{
			Type:   mounttypes.TypeBind,
			Source: a.deps.fs.Root(),
			Target: inContainerAgentRoot,
		},
		{
			Type:   mounttypes.TypeBind,
			Source: filepath.Join(a.deps.fs.Root(), "workspace"),
			Target: inContainerWorkspace,
		},
	}

	runtimeRef := a.runtimeRef(rt)
	if runtimeRef != "" {
		mounts = append(mounts, mounttypes.Mount{
			Type:     mounttypes.TypeImage,
			Source:   runtimeRef,
			Target:   inContainerRuntimeDir,
			ReadOnly: true,
		})
	}

	for name, ref := range bins {
		mounts = append(mounts, mounttypes.Mount{
			Type:     mounttypes.TypeImage,
			Source:   ref,
			Target:   inContainerBinsRoot + "/" + name,
			ReadOnly: true,
		})
	}

	// Propagate the agent's user-declared mounts (`otters run -v …`)
	// into the per-job container — without this, BINs that need
	// host-side credentials (kubectl + ~/.kube/config, gh + ~/.config/gh,
	// docker + ~/.docker, etc.) fall back to their no-config defaults
	// inside the per-job container even when the agent's main runtime
	// container has the mount. Mirrors agent.go's selection: prefer the
	// per-run RuntimeMounts when present, fall back to the
	// provider-level deps.mounts for callers still on docker.WithMounts.
	userMounts := a.deps.mounts
	if rt.Source != nil && rt.Source.Agent != nil && len(rt.Source.Agent.RuntimeMounts) > 0 {
		userMounts = make([]executor.Mount, 0, len(rt.Source.Agent.RuntimeMounts))
		for _, m := range rt.Source.Agent.RuntimeMounts {
			if m == nil {
				continue
			}
			userMounts = append(userMounts, executor.Mount{
				Host:        m.Host,
				Target:      m.Target,
				Description: m.Description,
				ReadOnly:    m.ReadOnly,
			})
		}
	}
	for _, m := range userMounts {
		mounts = append(mounts, mounttypes.Mount{
			Type:     mounttypes.TypeBind,
			Source:   m.Host,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}

	// Mirror containerSpec.buildHostConfig — per-job containers also
	// get the daemon socket mounted so chained jobs (an async job
	// that itself submits another job) can dial back.
	if a.deps.daemonSocket != "" {
		mounts = append(mounts, mounttypes.Mount{
			Type:   mounttypes.TypeBind,
			Source: a.deps.daemonSocket,
			Target: inContainerDaemonSocket,
		})
	}

	hostCfg := &containertypes.HostConfig{
		Mounts:     mounts,
		AutoRemove: false, // we remove explicitly so Inspect can read exit code
	}

	return cfg, hostCfg, nil
}

// binDeclared reports whether `name` is among the agent's declared
// BINs (resolved at Prepare time). Lookup is by exact name — same
// shape `BIN <name> <ref>` writes into the spec.
func binDeclared(rt *executor.Runtime, name string) bool {
	for _, t := range rt.Tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

// declaredBinNames returns the comma-separated list of BIN names the
// agent has, sorted for stable error messages. Used in the
// "BIN not declared" error so the operator immediately sees what IS
// available without grepping the Agentfile.
func declaredBinNames(rt *executor.Runtime) string {
	names := make([]string, 0, len(rt.Tools))
	for _, t := range rt.Tools {
		names = append(names, t.Name)
	}
	if len(names) == 0 {
		return "(none)"
	}
	// Local insertion sort — keeps the file dep-free; the list is
	// almost always under a dozen names.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j-1] > names[j]; j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}
	return strings.Join(names, ", ")
}

// ErrExecNotImplemented stays for backward compatibility with any
// caller that still imports it from the old stub. New code paths
// should use the real Exec implementation above.
//
// Deprecated: docker.Agent.Exec is now implemented; this var will
// be removed in a follow-up.
var ErrExecNotImplemented = errors.New("docker executor: Exec is now implemented; this error is unreachable")
