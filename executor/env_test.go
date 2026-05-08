package executor_test

import (
	"strings"
	"testing"

	"github.com/openotters/agentfile/executor"
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
