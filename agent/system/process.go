package system

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
// extra args. Kept as a pure function so the ordering contract (exec
// verb first, shared flags next, address, then extras) can be tested
// without spawning a subprocess.
func buildCmdArgs(rt *agent.AgentRuntime, rootDir string, extraArgs ...string) []string {
	execArgs := rt.Exec
	if len(execArgs) == 0 {
		execArgs = []string{"serve"}
	}

	args := append([]string{}, execArgs...)
	args = append(args, "--root", rootDir, "--model", rt.Model)

	if rt.APIBase != "" {
		args = append(args, "--api-base", rt.APIBase)
	}

	if rt.APIKey != "" {
		args = append(args, "--api-key", rt.APIKey)
	}

	if rt.Addr != "" {
		args = append(args, "--addr", rt.Addr)
	}

	args = append(args, extraArgs...)

	return args
}

func (p *process) buildCmdFn(rt *agent.AgentRuntime, rootDir string) cmdFunc {
	runtimeBin := filepath.Join(rootDir, RuntimeBin)
	stdout := p.stdout
	stderr := p.stderr
	executor := p.executor

	return func(extraArgs ...string) Cmd {
		c := executor.Command(runtimeBin, buildCmdArgs(rt, rootDir, extraArgs...)...)
		c.SetStdout(stdout)
		c.SetStderr(stderr)

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
