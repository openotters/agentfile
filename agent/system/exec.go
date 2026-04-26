package system

import (
	"fmt"
	"io"
	"os"
	osExec "os/exec"
)

// Executor produces a Cmd for a given binary + args. Abstracted so
// tests can substitute a scripted stub instead of spawning real
// subprocesses. Default implementation is defaultExecutor, which
// wraps os/exec.Command.
type Executor interface {
	Command(name string, args ...string) Cmd
}

// Cmd is the narrow slice of *os/exec.Cmd that process.serve actually
// needs: start, wait, deliver a signal, and wire stdout/stderr. The
// default implementation adapts *os/exec.Cmd.
//
// Signal implementations must be safe to call before Start and after
// Wait; in both cases they should return an error rather than panic,
// so process.signal's best-effort behaviour (signal then Kill on
// error) remains deterministic.
type Cmd interface {
	Start() error
	Wait() error
	Signal(sig os.Signal) error
	SetStdout(w io.Writer)
	SetStderr(w io.Writer)
	SetEnv(env []string)
}

// defaultExecutor is the production Executor: builds real
// *os/exec.Cmd values and wraps them in osCmd.
type defaultExecutor struct{}

func (defaultExecutor) Command(name string, args ...string) Cmd {
	// process.serve manages the lifecycle (signal-then-wait with
	// deadline) instead of relying on CommandContext's auto-kill.
	return &osCmd{cmd: osExec.Command(name, args...)} //nolint:noctx // see comment
}

// osCmd adapts *os/exec.Cmd to the Cmd interface. A zero-value
// osCmd.cmd is treated as "not started" by Signal so callers can't
// accidentally deref a nil Process.
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

func (c *osCmd) SetStdout(w io.Writer) { c.cmd.Stdout = w }
func (c *osCmd) SetStderr(w io.Writer) { c.cmd.Stderr = w }
func (c *osCmd) SetEnv(env []string)   { c.cmd.Env = env }
