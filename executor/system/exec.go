package system

import (
	"fmt"
	"io"
	"os"
	osExec "os/exec"
	"syscall"
)

// Spawner produces a Cmd for a binary and args. Abstracted so tests can
// substitute a scripted stub instead of spawning real subprocesses.
type Spawner interface {
	Command(name string, args ...string) Cmd
}

// Cmd is the slice of *os/exec.Cmd that process.serve needs: start, wait,
// signal, force-kill the process group, and wire stdio/env/dir. Signal and
// Kill must be safe to call before Start and after Wait, returning an error
// rather than panicking.
type Cmd interface {
	Start() error
	Wait() error
	Signal(sig os.Signal) error
	Kill() error
	SetStdout(w io.Writer)
	SetStderr(w io.Writer)
	SetEnv(env []string)
	SetDir(dir string)
}

// defaultSpawner is the production Spawner. It puts each runtime in its own
// process group so a force-kill reaps the tools it spawned, not just the
// runtime itself.
type defaultSpawner struct{}

func (defaultSpawner) Command(name string, args ...string) Cmd {
	cmd := osExec.Command(name, args...) //nolint:noctx // process.serve owns the lifecycle
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	return &osCmd{cmd: cmd}
}

// osCmd adapts *os/exec.Cmd to Cmd. A nil Process is treated as "not started".
type osCmd struct {
	cmd *osExec.Cmd
}

func (c *osCmd) Start() error { return c.cmd.Start() }
func (c *osCmd) Wait() error  { return c.cmd.Wait() }

func (c *osCmd) Signal(sig os.Signal) error {
	if c.cmd == nil || c.cmd.Process == nil {
		return fmt.Errorf("process not started")
	}

	return c.cmd.Process.Signal(sig)
}

// Kill force-kills the runtime's whole process group, so tools it spawned die
// with it. Falls back to killing just the process if the group send fails.
func (c *osCmd) Kill() error {
	if c.cmd == nil || c.cmd.Process == nil {
		return fmt.Errorf("process not started")
	}

	if err := syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}

	return c.cmd.Process.Kill()
}

func (c *osCmd) SetStdout(w io.Writer) { c.cmd.Stdout = w }
func (c *osCmd) SetStderr(w io.Writer) { c.cmd.Stderr = w }
func (c *osCmd) SetEnv(env []string)   { c.cmd.Env = env }
func (c *osCmd) SetDir(dir string)     { c.cmd.Dir = dir }
