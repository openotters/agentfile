package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/openotters/agentfile/executor"
	"github.com/openotters/agentfile/spec"
)

// Exec runs a BIN subprocess against the agent's already-materialized
// spawn env: same locked-down env vars, same workspace cwd, same PATH
// pointing at <rootDir>/usr/bin (where the agent's declared BINs are
// installed). Returns when the process exits or ctx is cancelled.
//
// The child runs in its own process group (Setpgid). On ctx
// cancellation we send SIGKILL to the whole group so any
// sub-processes the BIN itself spawned (`sh -c "a | b"` style)
// die with it — no orphaned grandchildren.
//
// The agent MUST be initialized (via Run / Start having reached the
// running state at least once) before Exec is called: the runtime
// descriptor populated at initialize time supplies the env. Calling
// Exec on an un-initialized agent returns an error in ExecResult.Err.
func (a *Agent) Exec(ctx context.Context, bin string, args []string, stdin string) executor.ExecResult {
	a.mu.Lock()
	rt := a.rt
	initialized := a.initialized
	a.mu.Unlock()

	if !initialized || rt == nil {
		return executor.ExecResult{
			Err: fmt.Errorf("system exec: agent %s not initialized — call Prepare/Run first", a.id),
		}
	}

	if a.fs == nil {
		return executor.ExecResult{Err: fmt.Errorf("system exec: agent %s has no filesystem", a.id)}
	}

	// bin becomes a filesystem path below; reject anything that could
	// traverse before it is joined onto the agent root.
	if !spec.IsPathSafeName(bin) {
		return executor.ExecResult{Err: fmt.Errorf("system exec: %q is not a valid BIN name", bin)}
	}

	// Fail fast when the requested BIN isn't declared. The declared list is
	// the source of truth; the chroot PATH is whatever materialise put there.
	if !systemBinDeclared(rt, bin) {
		return executor.ExecResult{
			Err: fmt.Errorf(
				"system exec: BIN %q is not declared in agent %s — "+
					"add `BIN %s <ref>` to its Agentfile and rebuild, or pick one of: %s",
				bin, a.id, bin, systemDeclaredBinNames(rt),
			),
		}
	}

	rootDir := a.fs.Root()
	binPath := filepath.Join(rootDir, "usr", "bin", bin)

	// Defensive belt-and-braces: the BIN is declared but maybe the
	// materialise step failed silently and the binary file isn't
	// actually on disk. Surface "BIN declared but missing on disk"
	// distinctly from "BIN not declared" so the operator knows which
	// way to fix it.
	if !filepathExistsRelative(rootDir, "usr", "bin", bin) {
		return executor.ExecResult{
			Err: fmt.Errorf(
				"system exec: BIN %q is declared but missing on disk in agent %s — "+
					"re-run materialise / restart the agent", bin, a.id),
		}
	}

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = filepath.Join(rootDir, "workspace")
	// Build the env under mu: it reads rt.APIBase/APIKey (mutated by
	// reresolveCredentials) and the daemon token (mutated by SetAgentToken).
	a.mu.Lock()
	cmd.Env = buildCmdEnv(rt, rootDir, a.daemonURL, a.agentToken)
	a.mu.Unlock()

	// Setpgid puts the child in its own pgid (== child PID). Cancel
	// path below sends SIGKILL to -pgid so children of `sh -c …` die.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	var stdout, stderr bytes.Buffer

	// Forward live bytes to the optional stream sinks (set by the
	// async-jobs pool so partial output lands in SQLite mid-flight).
	// Local buffers stay the source of truth for the final
	// ExecResult; the sinks are an additional, lossy channel.
	streamSinks := executor.ExecStreamSinksFrom(ctx)
	if streamSinks.Stdout != nil {
		cmd.Stdout = io.MultiWriter(&stdout, streamSinks.Stdout)
	} else {
		cmd.Stdout = &stdout
	}

	if streamSinks.Stderr != nil {
		cmd.Stderr = io.MultiWriter(&stderr, streamSinks.Stderr)
	} else {
		cmd.Stderr = &stderr
	}

	if err := cmd.Start(); err != nil {
		return executor.ExecResult{
			Stdout: stdout.String(),
			Stderr: stderr.String(),
			Err:    fmt.Errorf("system exec: start %s: %w", bin, err),
		}
	}

	pid := cmd.Process.Pid
	handle := strconv.Itoa(pid)

	// Race the wait against ctx cancellation. exec.CommandContext
	// already arranges a SIGKILL on cancel, but only to the leader —
	// we need the whole pgid down. Done in parallel with the wait so
	// cancelled jobs converge fast.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return wrapResult(stdout.String(), stderr.String(), err, handle, ctx.Err())
	case <-ctx.Done():
		// Kill the whole process group, then wait for the child.
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		select {
		case err := <-done:
			return wrapResult(stdout.String(), stderr.String(), err, handle, ctx.Err())
		case <-time.After(2 * time.Second):
			// Last resort: kill the leader directly. If the goroutine
			// is still wedged on Wait after this, it'll exit eventually
			// when the kernel reaps the process; we just stop blocking.
			_ = cmd.Process.Kill()
			return executor.ExecResult{
				Stdout: stdout.String(),
				Stderr: stderr.String(),
				Err:    ctx.Err(),
				Handle: handle,
			}
		}
	}
}

// wrapResult turns the captured output + Wait()'s error into an
// ExecResult. ExitError ⇒ exit code surfaced, Err nil. ctx error
// ⇒ Err = ctx.Err(). Other errors ⇒ Err with the cause.
func wrapResult(stdout, stderr string, waitErr error, handle string, ctxErr error) executor.ExecResult {
	out := executor.ExecResult{Stdout: stdout, Stderr: stderr, Handle: handle}
	if waitErr == nil {
		return out
	}
	exitErr := &exec.ExitError{}
	if errors.As(waitErr, &exitErr) {
		out.ExitCode = exitErr.ExitCode()
		// ctx-cancellation often surfaces as "signal: killed" exit;
		// promote it to Err so callers can distinguish "BIN exited
		// non-zero on its own" from "we killed it."
		if ctxErr != nil {
			out.Err = ctxErr
		}
		return out
	}
	if ctxErr != nil {
		out.Err = ctxErr
		return out
	}
	out.Err = waitErr
	return out
}

// filepathExistsRelative checks `<root>/<parts...>` for existence
// without traversing through a billy filesystem (which the chroot
// indirection would force). Plain os.Stat — same outcome.
func filepathExistsRelative(root string, parts ...string) bool {
	full := filepath.Join(append([]string{root}, parts...)...)
	_, err := osStat(full)
	return err == nil
}

// systemBinDeclared mirrors the docker backend's binDeclared check —
// same source of truth (rt.Tools), same fail-fast semantics. Kept
// per-package to avoid pulling docker imports into system or vice
// versa; promote to the executor package if a third caller appears.
func systemBinDeclared(rt *executor.Runtime, name string) bool {
	for _, t := range rt.Tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

func systemDeclaredBinNames(rt *executor.Runtime) string {
	names := make([]string, 0, len(rt.Tools))
	for _, t := range rt.Tools {
		names = append(names, t.Name)
	}
	if len(names) == 0 {
		return "(none)"
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j-1] > names[j]; j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}
	return strings.Join(names, ", ")
}
