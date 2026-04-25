//nolint:testpackage // direct internal access
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

	"github.com/openotters/agentfile/agent"
)

// --- hand-written stubs -------------------------------------------------

// Mockery-generated mocks live in mocks/system/ which imports
// agent/system, so tests inside package system can't use them (import
// cycle). For internal tests we hand-write tiny stubs — mockery is
// still the story for downstream consumers, but here we need direct
// access to `process` internals.

type stubCmd struct {
	mu       sync.Mutex
	stdout   io.Writer
	stderr   io.Writer
	startFn  func() error
	waitFn   func() error
	signals  []os.Signal
	signalFn func(os.Signal) error
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

func (s *stubCmd) SetStdout(w io.Writer) { s.stdout = w }
func (s *stubCmd) SetStderr(w io.Writer) { s.stderr = w }

func (s *stubCmd) signalsSent() []os.Signal {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]os.Signal, len(s.signals))
	copy(out, s.signals)
	return out
}

type stubExecutor struct {
	calls []stubCall
	cmd   *stubCmd
}

type stubCall struct {
	name string
	args []string
}

func (s *stubExecutor) Command(name string, args ...string) Cmd {
	s.calls = append(s.calls, stubCall{name: name, args: append([]string{}, args...)})
	return s.cmd
}

// --- defaultExecutor / osCmd --------------------------------------------

func TestDefaultExecutor_SignalBeforeStartErrors(t *testing.T) {
	t.Parallel()

	c := defaultExecutor{}.Command("/bin/true")

	// Before Start, .Process is nil. Signal must return an error, not
	// panic with a nil deref.
	if err := c.Signal(os.Interrupt); err == nil {
		t.Fatal("Signal(before Start) = nil, want error")
	}
}

func TestDefaultExecutor_WritesStdoutStderr(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skipf("/bin/sh not available: %v", err)
	}

	var out, errb bytes.Buffer

	c := defaultExecutor{}.Command("/bin/sh", "-c", "echo hi; echo oops >&2")
	c.SetStdout(&out)
	c.SetStderr(&errb)

	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := c.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if !strings.Contains(out.String(), "hi") {
		t.Fatalf("stdout = %q, want 'hi'", out.String())
	}

	if !strings.Contains(errb.String(), "oops") {
		t.Fatalf("stderr = %q, want 'oops'", errb.String())
	}
}

// --- process.serve via stubs --------------------------------------------

func TestProcessServe_HappyPath(t *testing.T) {
	t.Parallel()

	cmd := &stubCmd{}
	execer := &stubExecutor{cmd: cmd}

	p := &process{executor: execer, stdout: io.Discard, stderr: io.Discard}

	if err := p.serve(context.Background(), p.buildCmdFn(newTestRT(), "/r")); err != nil {
		t.Fatalf("serve: %v", err)
	}

	// Command was built with the expected runtime path + argv.
	if len(execer.calls) != 1 {
		t.Fatalf("executor.Command calls = %d, want 1", len(execer.calls))
	}

	call := execer.calls[0]
	if call.name != "/r/usr/local/bin/runtime" {
		t.Fatalf("Command name = %q, want /r/usr/local/bin/runtime", call.name)
	}

	wantArgs := []string{"serve", "--root", "/r", "--model", "m"}
	if !reflect.DeepEqual(call.args, wantArgs) {
		t.Fatalf("Command args = %q, want %q", call.args, wantArgs)
	}

	// stdout/stderr were wired from process to the cmd.
	if cmd.stdout != io.Discard || cmd.stderr != io.Discard {
		t.Fatalf("SetStdout/SetStderr not called")
	}
}

func TestProcessServe_StartError(t *testing.T) {
	t.Parallel()

	startErr := errors.New("binary not found")

	cmd := &stubCmd{startFn: func() error { return startErr }}
	execer := &stubExecutor{cmd: cmd}

	p := &process{executor: execer, stdout: io.Discard, stderr: io.Discard}

	err := p.serve(context.Background(), p.buildCmdFn(newTestRT(), "/r"))
	if err == nil || !strings.Contains(err.Error(), "starting runtime") {
		t.Fatalf("serve err = %v, want wrapped 'starting runtime'", err)
	}

	if !errors.Is(err, startErr) {
		t.Fatalf("inner error not propagated: %v", err)
	}
}

func TestProcessServe_ExitErrorPropagates(t *testing.T) {
	t.Parallel()

	exitErr := errors.New("exit status 42")

	cmd := &stubCmd{waitFn: func() error { return exitErr }}
	execer := &stubExecutor{cmd: cmd}

	p := &process{executor: execer, stdout: io.Discard, stderr: io.Discard}

	if err := p.serve(context.Background(), p.buildCmdFn(newTestRT(), "/r")); !errors.Is(err, exitErr) {
		t.Fatalf("serve err = %v, want %v", err, exitErr)
	}
}

func TestProcessServe_CtxCancelSignalsThenWaits(t *testing.T) {
	t.Parallel()

	// Wait blocks on a channel until the stub's Signal is delivered —
	// this lets us assert that ctx cancellation drives Signal first,
	// then the wait unblocks (mimicking a well-behaved subprocess that
	// exits on SIGINT).
	waitBlock := make(chan error, 1)

	cmd := &stubCmd{
		waitFn: func() error { return <-waitBlock },
		signalFn: func(_ os.Signal) error {
			waitBlock <- nil
			return nil
		},
	}
	execer := &stubExecutor{cmd: cmd}

	p := &process{executor: execer, stdout: io.Discard, stderr: io.Discard}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- p.serve(ctx, p.buildCmdFn(newTestRT(), "/r")) }()

	// Let serve register p.cmd + spawn the Wait goroutine.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve err = %v, want nil (clean SIGINT + clean Wait)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not return after ctx cancel")
	}

	sigs := cmd.signalsSent()
	if len(sigs) != 1 || sigs[0] != os.Interrupt {
		t.Fatalf("signals sent = %v, want [os.Interrupt]", sigs)
	}
}

// --- process.signal / stop ----------------------------------------------

func TestProcessSignal_SignalErrorEscalatesToKill(t *testing.T) {
	t.Parallel()

	cmd := &stubCmd{
		signalFn: func(sig os.Signal) error {
			if sig == os.Interrupt {
				return errors.New("no such process")
			}
			return nil
		},
	}

	p := &process{cmd: cmd}
	p.signal()

	sigs := cmd.signalsSent()
	if len(sigs) != 2 || sigs[0] != os.Interrupt || sigs[1] != os.Kill {
		t.Fatalf("signals = %v, want [Interrupt, Kill]", sigs)
	}
}

func TestProcessSignal_NilCmdIsNoop(t *testing.T) {
	t.Parallel()

	p := &process{cmd: nil}
	p.signal() // must not panic
}

func TestProcessStop(t *testing.T) {
	t.Parallel()

	// nil cancel → no-op, no panic.
	(&process{}).stop()

	// Non-nil cancel gets invoked.
	called := false
	p := &process{cancel: func() { called = true }}
	p.stop()

	if !called {
		t.Fatal("stop() did not invoke cancel")
	}
}

// --- helper -------------------------------------------------------------

// newTestRT returns a minimal AgentRuntime whose buildCmdArgs output is
// `serve --root /r --model m`, so every stub expectation can reuse the
// same argv literal.
func newTestRT() *agent.AgentRuntime {
	return &agent.AgentRuntime{
		ResolvedConfig: agent.ResolvedConfig{Model: "m"},
	}
}
