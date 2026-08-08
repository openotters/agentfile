//nolint:testpackage // exercises unexported lifecycle internals
package system

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/google/uuid"

	"github.com/openotters/agentfile/executor"
	"github.com/openotters/agentfile/oci"
)

// TestAgent_Run_FullLifecycle drives Run end-to-end against the real
// materialisation pipeline with a stubbed subprocess: Pulling →
// Starting → Stopped, with the runtime descriptor populated.
func TestAgent_Run_FullLifecycle(t *testing.T) {
	t.Parallel()

	store, ref := buildMaterializeFixture(t)

	a := NewAgent(uuid.New(), memfs.New(),
		WithStore(store),
		WithReference(ref),
		WithAgentPuller(oci.NoopPuller()),
		WithStaticModelResolver("http://localhost:9", "k"),
		WithAddr("127.0.0.1:59998"),
	)
	a.proc.spawner = &stubSpawner{cmd: &stubCmd{}}

	if err := a.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := a.Status(); got != executor.StatusStopped {
		t.Errorf("status = %v, want stopped", got)
	}

	if rt := a.Runtime(); rt == nil || rt.Name != "matz" {
		t.Errorf("runtime = %+v, want materialised fixture agent", rt)
	}

	if a.StatusTracker() == nil {
		t.Error("StatusTracker should never be nil")
	}
}

func TestAgent_Run_SpawnFailure(t *testing.T) {
	t.Parallel()

	store, ref := buildMaterializeFixture(t)

	a := NewAgent(uuid.New(), memfs.New(),
		WithStore(store),
		WithReference(ref),
		WithAgentPuller(oci.NoopPuller()),
		WithStaticModelResolver("http://localhost:9", "k"),
		WithAddr("127.0.0.1:59997"),
	)
	a.proc.spawner = &stubSpawner{cmd: &stubCmd{
		startFn: func() error { return errors.New("exec format error") },
	}}

	if err := a.Run(context.Background()); err == nil {
		t.Fatal("expected spawn failure")
	}
}

func TestAgent_SetAgentToken_ReachesNextSpawnEnv(t *testing.T) {
	t.Parallel()

	// The command is built from live state, so a token rotated between runs
	// must appear in the very next spawn's env — the regression this guards.
	cmd := &stubCmd{}
	a := NewAgent(uuid.New(), memfs.New(),
		WithDaemonURL("unix:///run/ottersd.sock"),
		WithAgentToken("old-jwt"),
		WithSpawner(&stubSpawner{cmd: cmd}),
	)
	a.markInitialized(&executor.Runtime{ResolvedConfig: executor.ResolvedConfig{Model: "anthropic/m"}})

	a.SetAgentToken("rotated-jwt")
	_ = a.command()

	if !slices.Contains(cmd.env, "OTTERS_AGENT_TOKEN=rotated-jwt") {
		t.Errorf("spawn env = %v, want the rotated token", cmd.env)
	}
}

func TestAgent_DaemonAndCapabilityOptions(t *testing.T) {
	t.Parallel()

	a := NewAgent(uuid.New(), memfs.New(),
		WithDaemonURL("unix:///tmp/ottersd.sock"),
		WithAgentToken("tok"),
		WithCapabilities([]executor.Capability{{Name: "note-save"}}),
	)

	if a.daemonURL != "unix:///tmp/ottersd.sock" || a.agentToken != "tok" {
		t.Errorf("daemon pair = %q/%q", a.daemonURL, a.agentToken)
	}

	if len(a.ws.capabilities) != 1 || a.ws.capabilities[0].Name != "note-save" {
		t.Errorf("capabilities = %+v", a.ws.capabilities)
	}
}

func TestExec_FailFastBranches(t *testing.T) {
	t.Parallel()

	t.Run("uninitialized agent", func(t *testing.T) {
		t.Parallel()

		a := NewAgent(uuid.New(), memfs.New())

		res := a.Exec(context.Background(), "jq", nil, "")
		if res.Err == nil || !strings.Contains(res.Err.Error(), "not initialized") {
			t.Fatalf("err = %v, want not-initialized", res.Err)
		}
	})

	t.Run("undeclared bin", func(t *testing.T) {
		t.Parallel()

		a := NewAgent(uuid.New(), memfs.New())
		a.markInitialized(&executor.Runtime{ResolvedConfig: executor.ResolvedConfig{
			Tools: []executor.ResolvedTool{{Name: "jq", Binary: "usr/bin/jq"}},
		}})

		res := a.Exec(context.Background(), "curl", nil, "")
		if res.Err == nil || !strings.Contains(res.Err.Error(), "not declared") {
			t.Fatalf("err = %v, want not-declared naming alternatives", res.Err)
		}

		if !strings.Contains(res.Err.Error(), "jq") {
			t.Errorf("error should list declared bins, got %v", res.Err)
		}
	})
}

func TestWrapResult(t *testing.T) {
	t.Parallel()

	t.Run("clean exit", func(t *testing.T) {
		t.Parallel()

		res := wrapResult("out", "err", nil, "h", nil)
		if res.Err != nil || res.ExitCode != 0 || res.Stdout != "out" || res.Handle != "h" {
			t.Errorf("res = %+v", res)
		}
	})

	t.Run("non-exit error", func(t *testing.T) {
		t.Parallel()

		boom := errors.New("boom")
		if res := wrapResult("", "", boom, "", nil); !errors.Is(res.Err, boom) {
			t.Errorf("err = %v, want boom", res.Err)
		}
	})

	t.Run("ctx cancellation wins", func(t *testing.T) {
		t.Parallel()

		res := wrapResult("", "", errors.New("signal: killed"), "", context.Canceled)
		if !errors.Is(res.Err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", res.Err)
		}
	})

	t.Run("exit error surfaces code", func(t *testing.T) {
		t.Parallel()

		// A real non-zero exit to obtain a genuine *exec.ExitError.
		cmd := exec.Command("false")
		waitErr := cmd.Run()

		res := wrapResult("", "", waitErr, "", nil)
		if res.Err != nil || res.ExitCode == 0 {
			t.Errorf("res = %+v, want exit code without Err", res)
		}
	})
}

func TestSystemBinHelpers(t *testing.T) {
	t.Parallel()

	rt := &executor.Runtime{ResolvedConfig: executor.ResolvedConfig{
		Tools: []executor.ResolvedTool{{Name: "wget"}, {Name: "jq"}},
	}}

	if !systemBinDeclared(rt, "jq") || systemBinDeclared(rt, "nope") {
		t.Error("systemBinDeclared verdicts wrong")
	}

	if got := systemDeclaredBinNames(rt); got != "jq, wget" {
		t.Errorf("declared names = %q, want sorted 'jq, wget'", got)
	}

	empty := &executor.Runtime{}
	if got := systemDeclaredBinNames(empty); got != "(none)" {
		t.Errorf("empty names = %q", got)
	}

	if filepathExistsRelative(t.TempDir(), "ghost") {
		t.Error("filepathExistsRelative should be false for missing path")
	}
}
