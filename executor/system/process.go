package system

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/openotters/agentfile/executor"
)

// cmdFunc builds a Cmd with optional extra arguments. The closure
// captures the Spawner, so the Cmd returned has already been wired
// up to the right spawner implementation (real os/exec in
// production, a mock in tests).
type cmdFunc func(extraArgs ...string) Cmd

// process manages a runtime subprocess.
type process struct {
	spawner Spawner
	stdout  io.Writer
	stderr  io.Writer

	mu     sync.Mutex
	cmd    Cmd
	cancel context.CancelFunc
}

// defaultExecVerb is the runtime subcommand the executor uses when an
// agent's spec doesn't override `Exec`. The runtime CLI registers
// `serve` (long-running gRPC server) as the only mode for spawned
// agents — everything else (`prompt`, `chat`) is wired through the
// gRPC API instead.
const defaultExecVerb = "serve"

// buildCmdArgs returns the full argv slice passed to the runtime
// binary, given the resolved runtime config and any caller-specified
// extra args. Credentials (api-key, api-base) are intentionally NOT
// on argv — they leak through `ps`. They travel via the subprocess
// env via buildCmdEnv.
func buildCmdArgs(rt *executor.Runtime, rootDir string, extraArgs ...string) []string {
	execArgs := rt.Exec
	if len(execArgs) == 0 {
		execArgs = []string{defaultExecVerb}
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
//   - OTTERS_AGENT_ROOT so tools can address the agent tree from
//     `sh -c` invocations
//   - <PROVIDER>_API_{KEY,BASE} for the resolved model
//
// SSH_AUTH_SOCK, AWS_*, GITHUB_TOKEN, the host's PATH, etc. stay on
// the host process — the agent never sees them.
//
// A model without a provider prefix (e.g. "bare-name") yields no
// credential entries — there is no provider to scope them under.
func buildCmdEnv(rt *executor.Runtime, rootDir, daemonURL, agentToken string) []string {
	env := executor.BuildLockedEnv(executor.EnvOptions{
		AgentRoot:  rootDir,
		BinDirs:    []string{filepath.Join(rootDir, "usr", "bin")},
		DaemonURL:  daemonURL,
		AgentToken: agentToken,
	})

	provider, _ := splitProviderPrefix(rt.Model)
	if provider != "" {
		prefix := strings.ToUpper(provider)

		if rt.APIKey != "" {
			env = append(env, prefix+"_API_KEY="+rt.APIKey)
		}

		if rt.APIBase != "" {
			env = append(env, prefix+"_API_BASE="+rt.APIBase)
		}
	}

	// User-declared ENV from the agentspec, last so it can shadow
	// nothing reserved (AppendUserEnv filters reserved keys; spec
	// validation already rejects them at build time).
	if rt.Source != nil && rt.Source.Agent != nil {
		env, _ = executor.AppendUserEnv(env, rt.Source.Agent.Envs)
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

func (p *process) buildCmdFn(rt *executor.Runtime, rootDir, daemonURL, agentToken string) cmdFunc {
	runtimeBin := filepath.Join(rootDir, RuntimeBin)
	stdout := p.stdout
	stderr := p.stderr
	spawner := p.spawner

	// Default CWD inside the agent's workspace dir so tools that
	// take relative paths (`cat ./foo`, `find . -type f`) resolve
	// where the agent expects.
	workspaceDir := filepath.Join(rootDir, "workspace")

	return func(extraArgs ...string) Cmd {
		args := buildCmdArgs(rt, rootDir, extraArgs...)

		c := spawner.Command(runtimeBin, args...)
		c.SetStdout(stdout)
		c.SetStderr(stderr)
		c.SetEnv(buildCmdEnv(rt, rootDir, daemonURL, agentToken))
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
