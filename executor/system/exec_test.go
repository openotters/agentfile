//nolint:testpackage // direct internal access to process/Cmd internals
package system

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openotters/agentfile/executor"
)

// stubCmd is a hand-written Cmd for exercising process internals. Mockery's
// generated mocks import executor/system, so package-internal tests can't use
// them without an import cycle.
type stubCmd struct {
	mu          sync.Mutex
	stdout      io.Writer
	stderr      io.Writer
	env         []string
	dir         string
	startFn     func() error
	waitFn      func() error
	signals     []os.Signal
	signalFn    func(os.Signal) error
	killed      bool
	killRelease chan struct{}
}

func (s *stubCmd) Start() error {
	if s.startFn != nil {
		return s.startFn()
	}

	return nil
}

func (s *stubCmd) Wait() error {
	if s.waitFn != nil {
		return s.waitFn()
	}

	return nil
}

func (s *stubCmd) Signal(sig os.Signal) error {
	s.mu.Lock()
	s.signals = append(s.signals, sig)
	fn := s.signalFn
	s.mu.Unlock()

	if fn != nil {
		return fn(sig)
	}

	return nil
}

func (s *stubCmd) Kill() error {
	s.mu.Lock()
	s.killed = true
	release := s.killRelease
	s.mu.Unlock()

	if release != nil {
		close(release)
	}

	return nil
}

func (s *stubCmd) SetStdout(w io.Writer) { s.stdout = w }
func (s *stubCmd) SetStderr(w io.Writer) { s.stderr = w }
func (s *stubCmd) SetEnv(env []string)   { s.env = env }
func (s *stubCmd) SetDir(dir string)     { s.dir = dir }

func (s *stubCmd) signalsSent() []os.Signal {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]os.Signal(nil), s.signals...)
}

func (s *stubCmd) wasKilled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.killed
}

type stubSpawner struct {
	calls []stubCall
	cmd   *stubCmd
}

type stubCall struct {
	name string
	args []string
}

func (s *stubSpawner) Command(name string, args ...string) Cmd {
	s.calls = append(s.calls, stubCall{name: name, args: append([]string{}, args...)})

	return s.cmd
}

func newTestProcess(cmd *stubCmd) (*process, *stubSpawner) {
	sp := &stubSpawner{cmd: cmd}

	return &process{spawner: sp, stdout: io.Discard, stderr: io.Discard}, sp
}

// newTestRT returns a minimal Runtime whose argv is `serve --root /r --model m`.
func newTestRT() *executor.Runtime {
	return &executor.Runtime{ResolvedConfig: executor.ResolvedConfig{Model: "m"}}
}

func TestDefaultSpawner_SignalAndKillBeforeStartError(t *testing.T) {
	t.Parallel()

	c := defaultSpawner{}.Command("/bin/true")

	if err := c.Signal(os.Interrupt); err == nil {
		t.Error("Signal before Start = nil, want error")
	}

	if err := c.Kill(); err == nil {
		t.Error("Kill before Start = nil, want error")
	}
}

func TestDefaultSpawner_WritesStdoutStderr(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skipf("/bin/sh not available: %v", err)
	}

	var out, errb bytes.Buffer

	c := defaultSpawner{}.Command("/bin/sh", "-c", "echo hi; echo oops >&2")
	c.SetStdout(&out)
	c.SetStderr(&errb)

	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := c.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if !strings.Contains(out.String(), "hi") || !strings.Contains(errb.String(), "oops") {
		t.Fatalf("stdout=%q stderr=%q", out.String(), errb.String())
	}
}

func TestProcessCommand_BuildsArgvAndWiresStdio(t *testing.T) {
	t.Parallel()

	cmd := &stubCmd{}
	p, sp := newTestProcess(cmd)

	built := p.command(newTestRT(), "/r", "", "")

	if built != cmd {
		t.Fatal("command did not return the spawner's cmd")
	}

	if len(sp.calls) != 1 || sp.calls[0].name != "/r/usr/local/bin/runtime" {
		t.Fatalf("spawn call = %+v", sp.calls)
	}

	want := []string{"serve", "--root", "/r", "--model", "m"}
	if !reflect.DeepEqual(sp.calls[0].args, want) {
		t.Fatalf("args = %q, want %q", sp.calls[0].args, want)
	}

	if cmd.stdout != io.Discard || cmd.stderr != io.Discard {
		t.Fatal("stdio not wired onto the cmd")
	}
}

func TestProcessServe_StartError(t *testing.T) {
	t.Parallel()

	startErr := errors.New("binary not found")
	p, _ := newTestProcess(&stubCmd{startFn: func() error { return startErr }})

	err := p.serve(context.Background(), p.command(newTestRT(), "/r", "", ""))
	if err == nil || !strings.Contains(err.Error(), "starting runtime") || !errors.Is(err, startErr) {
		t.Fatalf("serve err = %v, want wrapped start error", err)
	}
}

func TestProcessServe_ExitErrorPropagates(t *testing.T) {
	t.Parallel()

	exitErr := errors.New("exit status 42")
	p, _ := newTestProcess(&stubCmd{waitFn: func() error { return exitErr }})

	if err := p.serve(context.Background(), p.command(newTestRT(), "/r", "", "")); !errors.Is(err, exitErr) {
		t.Fatalf("serve err = %v, want %v", err, exitErr)
	}
}

func TestShutdown_CleanExitOnSignal(t *testing.T) {
	t.Parallel()

	// A well-behaved runtime exits on SIGINT before the grace elapses.
	waitBlock := make(chan error, 1)
	cmd := &stubCmd{
		waitFn:   func() error { return <-waitBlock },
		signalFn: func(os.Signal) error { waitBlock <- nil; return nil },
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	if err := shutdown(cmd, done, time.Second); err != nil {
		t.Fatalf("shutdown err = %v", err)
	}

	if sigs := cmd.signalsSent(); len(sigs) != 1 || sigs[0] != os.Interrupt {
		t.Fatalf("signals = %v, want [Interrupt]", sigs)
	}

	if cmd.wasKilled() {
		t.Error("clean exit should not escalate to Kill")
	}
}

func TestShutdown_EscalatesToKillAfterGrace(t *testing.T) {
	t.Parallel()

	// A runtime that ignores SIGINT stays blocked in Wait until Kill releases
	// it, mimicking SIGKILL reaping the process group.
	release := make(chan struct{})
	cmd := &stubCmd{
		killRelease: release,
		waitFn:      func() error { <-release; return nil },
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	if err := shutdown(cmd, done, 10*time.Millisecond); err != nil {
		t.Fatalf("shutdown err = %v", err)
	}

	if !cmd.wasKilled() {
		t.Error("wedged runtime was not force-killed after grace")
	}

	if sigs := cmd.signalsSent(); len(sigs) != 1 || sigs[0] != os.Interrupt {
		t.Fatalf("signals = %v, want [Interrupt] before Kill", sigs)
	}
}
