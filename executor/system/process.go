package system

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openotters/agentfile/executor"
)

// defaultExecVerb is the runtime subcommand used when an agent's spec omits
// EXEC. The runtime CLI registers `serve` as the only mode for spawned agents.
const defaultExecVerb = "serve"

// shutdownGrace is how long serve waits after SIGINT before escalating to a
// force-kill of the runtime's process group.
const shutdownGrace = 10 * time.Second

// process spawns and supervises a runtime subprocess. It holds no per-run
// state; the Agent owns the run lifecycle (cancellation, the ran channel).
type process struct {
	spawner Spawner
	stdout  io.Writer
	stderr  io.Writer
}

// command builds a wired Cmd from live agent state. Credentials never go on
// argv (they leak through `ps`); they travel via the spawn env.
func (p *process) command(rt *executor.Runtime, rootDir, daemonURL, agentToken string) Cmd {
	c := p.spawner.Command(filepath.Join(rootDir, RuntimeBin), buildCmdArgs(rt, rootDir)...)
	c.SetStdout(p.stdout)
	c.SetStderr(p.stderr)
	c.SetEnv(buildCmdEnv(rt, rootDir, daemonURL, agentToken))
	c.SetDir(filepath.Join(rootDir, "workspace"))

	return c
}

// serve starts cmd and blocks until it exits or ctx is cancelled. On
// cancellation it sends SIGINT, then force-kills the process group if the
// runtime does not exit within shutdownGrace.
func (p *process) serve(ctx context.Context, cmd Cmd) error {
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting runtime: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return shutdown(cmd, done, shutdownGrace)
	}
}

// shutdown asks cmd to exit with SIGINT and force-kills its process group if it
// does not exit within grace.
func shutdown(cmd Cmd, done <-chan error, grace time.Duration) error {
	_ = cmd.Signal(os.Interrupt)

	select {
	case err := <-done:
		return err
	case <-time.After(grace):
		_ = cmd.Kill()

		return <-done
	}
}

// buildCmdArgs returns the runtime argv: the EXEC verb (default `serve`)
// followed by the executor-appended --root/--model/--addr flags.
func buildCmdArgs(rt *executor.Runtime, rootDir string) []string {
	execArgs := rt.Exec
	if len(execArgs) == 0 {
		execArgs = []string{defaultExecVerb}
	}

	args := append([]string{}, execArgs...)
	args = append(args, "--root", rootDir)

	if rt.Model != "" {
		args = append(args, "--model", rt.Model)
	}

	if rt.Addr != "" {
		args = append(args, "--addr", rt.Addr)
	}

	return args
}

// buildCmdEnv returns the locked-down spawn environment: no host inheritance,
// plus provider credentials, the CONFIG export, and user ENV (ENV last so it
// wins on collision). See executor.BuildLockedEnv.
func buildCmdEnv(rt *executor.Runtime, rootDir, daemonURL, agentToken string) []string {
	env := executor.BuildLockedEnv(executor.EnvOptions{
		AgentRoot:  rootDir,
		BinDirs:    []string{filepath.Join(rootDir, "usr", "bin")},
		DaemonURL:  daemonURL,
		AgentToken: agentToken,
	})

	if provider, _ := splitProviderPrefix(rt.Model); provider != "" {
		prefix := strings.ToUpper(provider)
		if rt.APIKey != "" {
			env = append(env, prefix+"_API_KEY="+rt.APIKey)
		}
		if rt.APIBase != "" {
			env = append(env, prefix+"_API_BASE="+rt.APIBase)
		}
	}

	env = executor.AppendConfigEnv(env, rt.Configs)
	env, _ = executor.AppendUserEnv(env, rt.Envs)

	return env
}

// splitProviderPrefix splits "provider/model" at the first slash, returning
// ("", model) when there is no provider prefix.
func splitProviderPrefix(model string) (string, string) {
	if idx := strings.Index(model, "/"); idx > 0 {
		return model[:idx], model[idx+1:]
	}

	return "", model
}
