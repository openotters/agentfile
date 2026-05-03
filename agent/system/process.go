package system

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/openotters/agentfile/agent"
)

// cmdFunc builds a Cmd with optional extra arguments. The closure
// captures the Executor, so the Cmd returned has already been wired
// up to the right executor implementation (real os/exec in
// production, a mock in tests).
type cmdFunc func(extraArgs ...string) Cmd

// process manages a runtime subprocess.
type process struct {
	executor Executor
	stdout   io.Writer
	stderr   io.Writer

	mu     sync.Mutex
	cmd    Cmd
	cancel context.CancelFunc
}

// buildCmdArgs returns the full argv slice passed to the runtime
// binary, given the resolved runtime config and any caller-specified
// extra args. Credentials (api-key, api-base) are intentionally NOT
// on argv — they leak through `ps`. They travel via the subprocess
// env via buildCmdEnv.
func buildCmdArgs(rt *agent.AgentRuntime, rootDir string, extraArgs ...string) []string {
	execArgs := rt.Exec
	if len(execArgs) == 0 {
		execArgs = []string{"serve"}
	}

	args := append([]string{}, execArgs...)
	args = append(args, "--root", rootDir, "--model", rt.Model)

	if rt.Addr != "" {
		args = append(args, "--addr", rt.Addr)
	}

	args = append(args, extraArgs...)

	return args
}

// buildCmdEnv returns the locked-down environment for the runtime
// subprocess, plus per-provider credential entries
// (<PROVIDER>_API_KEY / <PROVIDER>_API_BASE) when the resolved values
// are non-empty.
//
// Crucially this does NOT inherit os.Environ — the runtime spawn
// (and every tool subprocess that forks from it) sees only:
//
//   - PATH = <rootDir>/usr/bin   (the agent's pinned BIN tools, not
//     the host's)
//   - HOME / XDG_* / TMPDIR rooted inside the agent tree
//   - LANG = C.UTF-8 for predictable locale output
//   - OTTERS_AGENT_ROOT + OTTERS_MOUNTS so the runtime can render
//     Workspace.md from real paths
//   - <PROVIDER>_API_{KEY,BASE} for the resolved model
//
// SSH_AUTH_SOCK, AWS_*, GITHUB_TOKEN, the host's PATH, etc. stay on
// the host process — the agent never sees them.
//
// A model without a provider prefix (e.g. "bare-name") yields no
// credential entries — there is no provider to scope them under.
func buildCmdEnv(rt *agent.AgentRuntime, rootDir string, mounts []Mount) []string {
	env := BuildLockedEnv(rootDir, mounts)

	provider, _ := splitProviderPrefix(rt.Model)
	if provider == "" {
		return env
	}

	prefix := strings.ToUpper(provider)

	if rt.APIKey != "" {
		env = append(env, prefix+"_API_KEY="+rt.APIKey)
	}

	if rt.APIBase != "" {
		env = append(env, prefix+"_API_BASE="+rt.APIBase)
	}

	return env
}

// splitProviderPrefix returns the part before the first '/' in a
// fully-qualified model name (e.g. "anthropic/claude-..." → "anthropic").
// Returns "" when the model has no slash. Mirrors openotters/internal's
// splitModel — duplicated locally to avoid the agentfile→openotters
// import cycle. Promote to a shared helper if a third caller appears.
func splitProviderPrefix(model string) (string, string) {
	if idx := strings.Index(model, "/"); idx > 0 {
		return model[:idx], model[idx+1:]
	}

	return "", model
}

func (p *process) buildCmdFn(rt *agent.AgentRuntime, rootDir string, mounts []Mount) cmdFunc {
	runtimeBin := filepath.Join(rootDir, RuntimeBin)
	stdout := p.stdout
	stderr := p.stderr
	executor := p.executor

	// Build the per-OS sandbox wrapper once at cmdFunc construction
	// time so its profile / argv work happens off the hot path.
	sb := sandboxFor(sandboxParamsFor(rt, mounts, rootDir, runtimeBin))

	// Default CWD inside the agent's workspace dir so tools that
	// take relative paths (`cat ./foo`, `find . -type f`) resolve
	// where the agent expects.
	workspaceDir := filepath.Join(rootDir, "workspace")

	return func(extraArgs ...string) Cmd {
		argv := append([]string{runtimeBin}, buildCmdArgs(rt, rootDir, extraArgs...)...)
		argv = sb.Wrap(argv)

		c := executor.Command(argv[0], argv[1:]...)
		c.SetStdout(stdout)
		c.SetStderr(stderr)
		c.SetEnv(buildCmdEnv(rt, rootDir, mounts))
		c.SetDir(workspaceDir)

		return c
	}
}

func (p *process) serve(ctx context.Context, cmdFn cmdFunc) error {
	ctx, cancel := context.WithCancel(ctx)

	p.mu.Lock()
	p.cancel = cancel
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.cancel = nil
		p.mu.Unlock()
	}()

	cmd := cmdFn()

	p.mu.Lock()
	p.cmd = cmd
	p.mu.Unlock()

	if startErr := cmd.Start(); startErr != nil {
		return fmt.Errorf("starting runtime: %w", startErr)
	}

	done := make(chan error, 1)

	go func() { done <- cmd.Wait() }()

	select {
	case runErr := <-done:
		return runErr
	case <-ctx.Done():
		p.signal()

		return <-done
	}
}

func (p *process) stop() {
	p.mu.Lock()
	cancel := p.cancel
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

func (p *process) signal() {
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()

	if cmd == nil {
		return
	}

	if err := cmd.Signal(os.Interrupt); err != nil {
		// Best-effort escalation: if SIGINT can't be delivered (not
		// started, already exited, …), try SIGKILL. Final error is
		// swallowed — signal() is documented as best-effort and the
		// serve loop will observe the exit via Wait.
		_ = cmd.Signal(os.Kill)
	}
}
