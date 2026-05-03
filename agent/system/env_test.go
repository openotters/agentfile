//nolint:testpackage // direct internal access
package system

import (
	"strings"
	"testing"
)

func TestBuildLockedEnv_OnlyCuratedKeys(t *testing.T) {
	t.Parallel()

	env := BuildLockedEnv("/agents/abc", nil)

	want := []string{
		"PATH=/agents/abc/usr/bin",
		"HOME=/agents/abc/home",
		"XDG_CONFIG_HOME=/agents/abc/home/.config",
		"XDG_CACHE_HOME=/agents/abc/home/.cache",
		"XDG_DATA_HOME=/agents/abc/home/.local/share",
		"TMPDIR=/agents/abc/tmp",
		"LANG=C.UTF-8",
		"OTTERS_AGENT_ROOT=/agents/abc",
		"OTTERS_MOUNTS=[]",
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

func TestBuildLockedEnv_MountsSerialiseAsJSON(t *testing.T) {
	t.Parallel()

	mounts := []Mount{
		{Host: "/Users/me/proj", Target: "/workspace/proj", Description: "src"},
		{Host: "/tmp/data", Target: "/data"},
	}

	env := BuildLockedEnv("/agents/abc", mounts)

	for _, e := range env {
		if !strings.HasPrefix(e, "OTTERS_MOUNTS=") {
			continue
		}
		val := strings.TrimPrefix(e, "OTTERS_MOUNTS=")
		// JSON shape, not byte-exact: must be a non-empty array
		// containing both host paths.
		if !strings.Contains(val, `"host":"/Users/me/proj"`) ||
			!strings.Contains(val, `"host":"/tmp/data"`) {
			t.Errorf("OTTERS_MOUNTS missing one of the host paths: %q", val)
		}
		return
	}
	t.Errorf("OTTERS_MOUNTS not found in env: %v", env)
}

func TestBuildLockedEnv_NoHostInheritance(t *testing.T) {
	// Spike a host env var BuildLockedEnv must NOT pass through.
	// No t.Parallel — Setenv mutates the test process env.
	t.Setenv("OTTERS_TEST_HOST_LEAK_CANARY", "leaked")

	env := BuildLockedEnv("/agents/abc", nil)

	for _, e := range env {
		if strings.HasPrefix(e, "OTTERS_TEST_HOST_LEAK_CANARY=") {
			t.Fatalf("BuildLockedEnv leaked host env: %q", e)
		}
	}
}

func TestEnvIndex_Helper(t *testing.T) {
	t.Parallel()

	env := []string{"FOO=1", "BAR=2", "HOME=/agents/x"}

	if got := envIndex(env, "BAR"); got != 1 {
		t.Errorf("envIndex(BAR) = %d, want 1", got)
	}
	if got := envIndex(env, "MISSING"); got != -1 {
		t.Errorf("envIndex(MISSING) = %d, want -1", got)
	}
	// Prefix match must be exact: "HOM" should not match "HOME=...".
	if got := envIndex(env, "HOM"); got != -1 {
		t.Errorf("envIndex(HOM) = %d, want -1 (prefix should require '=')", got)
	}
}
