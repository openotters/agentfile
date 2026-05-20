package executor_test

import (
	"strings"
	"testing"

	"github.com/openotters/agentfile/executor"
	"github.com/openotters/agentfile/spec"
)

func TestBuildLockedEnv_OnlyCuratedKeys(t *testing.T) {
	t.Parallel()

	env := executor.BuildLockedEnv(executor.EnvOptions{
		AgentRoot: "/agents/abc",
		BinDirs:   []string{"/agents/abc/usr/bin"},
	})

	want := []string{
		"PATH=/agents/abc/usr/bin",
		"HOME=/agents/abc/home",
		"XDG_CONFIG_HOME=/agents/abc/home/.config",
		"XDG_CACHE_HOME=/agents/abc/home/.cache",
		"XDG_DATA_HOME=/agents/abc/home/.local/share",
		"TMPDIR=/agents/abc/tmp",
		"LANG=C.UTF-8",
		"OTTERS_AGENT_ROOT=/agents/abc",
	}

	if len(env) != len(want) {
		t.Errorf("env has %d entries; want %d (%v)", len(env), len(want), env)
	}

	for _, w := range want {
		found := false
		for _, e := range env {
			if e == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing entry %q in env: %v", w, env)
		}
	}
}

func TestBuildLockedEnv_MultipleBinDirsJoinedWithColon(t *testing.T) {
	t.Parallel()

	env := executor.BuildLockedEnv(executor.EnvOptions{
		AgentRoot: "/workspace",
		BinDirs:   []string{"/opt/bins/ping", "/opt/bins/jq"},
	})

	want := "PATH=/opt/bins/ping:/opt/bins/jq"
	for _, e := range env {
		if e == want {
			return
		}
	}
	t.Errorf("missing %q in env: %v", want, env)
}

func TestAppendUserEnv_AppendsInOrder(t *testing.T) {
	t.Parallel()

	base := []string{"PATH=/usr/bin", "HOME=/agents/x/home"}
	envs := []*spec.Env{
		{Key: "NODE_ENV", Value: "production"},
		{Key: "LOG_LEVEL", Value: "debug"},
	}

	got, skipped := executor.AppendUserEnv(base, envs)
	if len(skipped) != 0 {
		t.Errorf("skipped = %v, want none", skipped)
	}

	want := []string{"PATH=/usr/bin", "HOME=/agents/x/home", "NODE_ENV=production", "LOG_LEVEL=debug"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAppendUserEnv_FiltersReservedKeys(t *testing.T) {
	t.Parallel()

	base := []string{"PATH=/usr/bin"}
	envs := []*spec.Env{
		{Key: "PATH", Value: "/evil"},
		{Key: "HOME", Value: "/evil"},
		{Key: "OTTERS_AGENT_ROOT", Value: "/evil"},
		{Key: "STRIPE_API_KEY", Value: "sk_test"},
		{Key: "OPENAI_API_BASE", Value: "https://evil"},
		{Key: "NODE_ENV", Value: "production"},
	}

	got, skipped := executor.AppendUserEnv(base, envs)

	wantSkipped := []string{"PATH", "HOME", "OTTERS_AGENT_ROOT", "STRIPE_API_KEY", "OPENAI_API_BASE"}
	if len(skipped) != len(wantSkipped) {
		t.Fatalf("skipped = %v, want %v", skipped, wantSkipped)
	}
	for i, k := range wantSkipped {
		if skipped[i] != k {
			t.Errorf("skipped[%d] = %q, want %q", i, skipped[i], k)
		}
	}

	// Only the legal entry survived.
	want := []string{"PATH=/usr/bin", "NODE_ENV=production"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAppendUserEnv_NilOrEmpty(t *testing.T) {
	t.Parallel()

	base := []string{"PATH=/usr/bin"}

	got1, skipped1 := executor.AppendUserEnv(base, nil)
	if len(skipped1) != 0 || len(got1) != 1 || got1[0] != "PATH=/usr/bin" {
		t.Errorf("nil: got=%v skipped=%v", got1, skipped1)
	}

	got2, _ := executor.AppendUserEnv(base, []*spec.Env{nil, {Key: ""}, {Key: "OK", Value: "v"}})
	want2 := []string{"PATH=/usr/bin", "OK=v"}
	if len(got2) != len(want2) {
		t.Fatalf("got %v, want %v", got2, want2)
	}
}

func TestAppendConfigEnv_EmitsRuntimePrefixedKeys(t *testing.T) {
	t.Parallel()

	base := []string{"PATH=/usr/bin"}

	configs := map[string]string{
		"max-tokens":      "2048",
		"memory-strategy": "summarize",
		"temperature":     "0.7",
	}

	got := executor.AppendConfigEnv(base, configs)

	// Output is sorted by key so the test can assert exact ordering.
	want := []string{
		"PATH=/usr/bin",
		"RUNTIME_MAX_TOKENS=2048",
		"RUNTIME_MEMORY_STRATEGY=summarize",
		"RUNTIME_TEMPERATURE=0.7",
	}
	if len(got) != len(want) {
		t.Fatalf("len: got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("env[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestAppendConfigEnv_EmptyOrNilLeavesBaseUntouched(t *testing.T) {
	t.Parallel()

	base := []string{"PATH=/usr/bin"}

	got := executor.AppendConfigEnv(base, nil)
	if len(got) != 1 || got[0] != "PATH=/usr/bin" {
		t.Errorf("nil configs: got %v", got)
	}

	got = executor.AppendConfigEnv(base, map[string]string{})
	if len(got) != 1 || got[0] != "PATH=/usr/bin" {
		t.Errorf("empty configs: got %v", got)
	}

	// Empty keys are skipped — they'd produce a bare "RUNTIME_=value"
	// which isn't a useful identifier and tools wouldn't read it.
	got = executor.AppendConfigEnv(base, map[string]string{"": "ignored", "ok": "v"})
	if len(got) != 2 || got[1] != "RUNTIME_OK=v" {
		t.Errorf("empty-key skip: got %v", got)
	}
}

func TestBuildLockedEnv_NoHostInheritance(t *testing.T) {
	// Spike a host env var BuildLockedEnv must NOT pass through.
	// No t.Parallel — Setenv mutates the test process env.
	t.Setenv("OTTERS_TEST_HOST_LEAK_CANARY", "leaked")

	env := executor.BuildLockedEnv(executor.EnvOptions{
		AgentRoot: "/agents/abc",
		BinDirs:   []string{"/agents/abc/usr/bin"},
	})

	for _, e := range env {
		if strings.HasPrefix(e, "OTTERS_TEST_HOST_LEAK_CANARY=") {
			t.Fatalf("BuildLockedEnv leaked host env: %q", e)
		}
	}
}
