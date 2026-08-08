//nolint:testpackage // direct internal access
package system

import (
	"reflect"
	"slices"
	"testing"

	"github.com/openotters/agentfile/executor"
	"github.com/openotters/agentfile/spec"
)

func TestBuildCmdArgs_DefaultsToServe(t *testing.T) {
	t.Parallel()

	got := buildCmdArgs(&executor.Runtime{ResolvedConfig: executor.ResolvedConfig{Model: "anthropic/claude"}}, "/root")

	want := []string{"serve", "--root", "/root", "--model", "anthropic/claude"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestBuildCmdArgs_CustomExecVerb(t *testing.T) {
	t.Parallel()

	got := buildCmdArgs(&executor.Runtime{
		ResolvedConfig: executor.ResolvedConfig{
			Model: "m",
			Exec:  []string{"prompt", "--verbose"},
		},
	}, "/root")

	// Custom exec args come first, then the shared --root/--model pair.
	want := []string{"prompt", "--verbose", "--root", "/root", "--model", "m"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestBuildCmdArgs_DoesNotEmitCredentialFlags(t *testing.T) {
	t.Parallel()

	// Credentials must travel via env, never argv (would leak via `ps`).
	// Assert no --api-key / --api-base regardless of whether the rt has
	// values populated.
	got := buildCmdArgs(&executor.Runtime{
		ResolvedConfig: executor.ResolvedConfig{
			Model:   "openai/gpt-4",
			APIBase: "https://api.example.com",
			APIKey:  "sk-secret",
		},
	}, "/root")

	for _, forbidden := range []string{"--api-key", "--api-base", "sk-secret", "https://api.example.com"} {
		if slices.Contains(got, forbidden) {
			t.Errorf("argv leaks %q: %q", forbidden, got)
		}
	}
}

func TestBuildCmdArgs_Addr(t *testing.T) {
	t.Parallel()

	got := buildCmdArgs(&executor.Runtime{
		ResolvedConfig: executor.ResolvedConfig{Model: "m", Addr: "127.0.0.1:9999"},
	}, "/root")

	want := []string{"serve", "--root", "/root", "--model", "m", "--addr", "127.0.0.1:9999"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestBuildCmdArgs_FullComposition(t *testing.T) {
	t.Parallel()

	// Exec + base flags + addr; credentials are excluded by design (see
	// TestBuildCmdArgs_DoesNotEmitCredentialFlags) — they flow via the env.
	got := buildCmdArgs(&executor.Runtime{
		ResolvedConfig: executor.ResolvedConfig{
			Model:   "openai/gpt-4",
			APIBase: "https://api.openai.com",
			APIKey:  "sk-xxx",
			Addr:    "127.0.0.1:1234",
			Exec:    []string{"serve"},
		},
	}, "/root")

	want := []string{
		"serve",
		"--root", "/root",
		"--model", "openai/gpt-4",
		"--addr", "127.0.0.1:1234",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// envHas asserts that env contains a `KEY=value` entry exactly.
func envHas(t *testing.T, env []string, want string) {
	t.Helper()
	if !slices.Contains(env, want) {
		t.Errorf("env missing %q; got %v", want, env)
	}
}

// envHasKey asserts that env contains an entry starting with "KEY=".
// Used when the value is constructed (e.g. paths) and only the key
// matters.
func envHasKey(t *testing.T, env []string, key string) {
	t.Helper()
	prefix := key + "="
	for _, e := range env {
		if hasPrefix(e, prefix) {
			return
		}
	}
	t.Errorf("env missing key %q; got %v", key, env)
}

// envHasNoKey asserts that env contains no entry starting with "KEY=".
func envHasNoKey(t *testing.T, env []string, key string) {
	t.Helper()
	prefix := key + "="
	for _, e := range env {
		if hasPrefix(e, prefix) {
			t.Errorf("env unexpectedly has %q; got %v", e, env)
			return
		}
	}
}

func TestBuildCmdEnv_LockedBaseAlwaysPresent(t *testing.T) {
	t.Parallel()

	// Every spawn gets PATH / HOME / XDG_* / TMPDIR / LANG /
	// OTTERS_AGENT_ROOT — the curated locked-down env. The host's
	// PATH / HOME / arbitrary env vars stay on the host; the runtime
	// sees only this list (plus per-provider credentials).
	env := buildCmdEnv(&executor.Runtime{
		ResolvedConfig: executor.ResolvedConfig{Model: "anthropic/m"},
	}, "/agents/abc", "", "")

	envHas(t, env, "PATH=/agents/abc/usr/bin")
	envHas(t, env, "HOME=/agents/abc/home")
	envHas(t, env, "XDG_CONFIG_HOME=/agents/abc/home/.config")
	envHas(t, env, "XDG_CACHE_HOME=/agents/abc/home/.cache")
	envHas(t, env, "XDG_DATA_HOME=/agents/abc/home/.local/share")
	envHas(t, env, "TMPDIR=/agents/abc/tmp")
	envHas(t, env, "LANG=C.UTF-8")
	envHas(t, env, "OTTERS_AGENT_ROOT=/agents/abc")
}

func TestBuildCmdEnv_DoesNotInheritHostEnv(t *testing.T) {
	// No t.Parallel — we mutate the test process's env via Setenv,
	// which testing.T forbids in parallel tests.

	// Spike a host env var that the locked-down env explicitly
	// does NOT include. If the implementation regresses to
	// os.Environ inheritance, this canary will fail.
	t.Setenv("OTTERS_TEST_HOST_CANARY", "leaked")

	env := buildCmdEnv(&executor.Runtime{
		ResolvedConfig: executor.ResolvedConfig{Model: "anthropic/m"},
	}, "/r", "", "")

	envHasNoKey(t, env, "OTTERS_TEST_HOST_CANARY")
	// Common host-only env vars also shouldn't leak.
	envHasNoKey(t, env, "SSH_AUTH_SOCK")
}

func TestBuildCmdEnv_EmitsProviderCredentials(t *testing.T) {
	t.Parallel()

	rt := &executor.Runtime{
		ResolvedConfig: executor.ResolvedConfig{
			Model:   "anthropic/claude-sonnet-4-5",
			APIBase: "https://api.anthropic.com",
			APIKey:  "sk-test-fixture-not-real",
		},
	}

	env := buildCmdEnv(rt, "/r", "", "")

	envHas(t, env, "ANTHROPIC_API_KEY=sk-test-fixture-not-real")
	envHas(t, env, "ANTHROPIC_API_BASE=https://api.anthropic.com")
}

func TestBuildCmdEnv_OmitsAbsentFields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		rt          *executor.Runtime
		mustNotHave []string
	}{
		{
			name: "no key",
			rt: &executor.Runtime{ResolvedConfig: executor.ResolvedConfig{
				Model: "anthropic/m", APIBase: "https://x",
			}},
			mustNotHave: []string{"ANTHROPIC_API_KEY"},
		},
		{
			name: "no base",
			rt: &executor.Runtime{ResolvedConfig: executor.ResolvedConfig{
				Model: "anthropic/m", APIKey: "k",
			}},
			mustNotHave: []string{"ANTHROPIC_API_BASE"},
		},
		{
			name:        "neither",
			rt:          &executor.Runtime{ResolvedConfig: executor.ResolvedConfig{Model: "anthropic/m"}},
			mustNotHave: []string{"ANTHROPIC_API_KEY", "ANTHROPIC_API_BASE"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env := buildCmdEnv(tc.rt, "/r", "", "")
			for _, key := range tc.mustNotHave {
				envHasNoKey(t, env, key)
			}
		})
	}
}

func TestBuildCmdEnv_NoProviderInModel(t *testing.T) {
	t.Parallel()

	// A bare model name (no "provider/") yields no provider-scoped
	// credential entries — but the locked-down base env is still
	// fully present.
	env := buildCmdEnv(&executor.Runtime{
		ResolvedConfig: executor.ResolvedConfig{
			Model:   "bare-name",
			APIKey:  "k",
			APIBase: "https://x",
		},
	}, "/r", "", "")

	envHasKey(t, env, "PATH")
	envHasKey(t, env, "HOME")
	envHasKey(t, env, "OTTERS_AGENT_ROOT")
	// No provider prefix → no API_KEY / API_BASE entries.
	for _, e := range env {
		if hasPrefix(e, "BARE") || hasPrefix(e, "_API_KEY=") || hasPrefix(e, "_API_BASE=") {
			t.Errorf("unexpected provider-style entry %q", e)
		}
	}
}

func TestBuildCmdEnv_AppendsUserEnv(t *testing.T) {
	t.Parallel()

	rt := &executor.Runtime{
		ResolvedConfig: executor.ResolvedConfig{
			Model:  "anthropic/m",
			APIKey: "k",
			Envs: []*spec.Env{
				{Key: "NODE_ENV", Value: "production"},
				{Key: "FEATURE_X", Value: "on"},
				// Reserved keys would normally be rejected by
				// spec.Validate; AppendUserEnv filters defensively.
				{Key: "PATH", Value: "/evil"},
				{Key: "STRIPE_API_KEY", Value: "sk_test"},
			},
		},
	}

	env := buildCmdEnv(rt, "/r", "", "")

	envHas(t, env, "NODE_ENV=production")
	envHas(t, env, "FEATURE_X=on")
	// PATH from the locked-down base survives; the user-declared
	// override is filtered.
	envHas(t, env, "PATH=/r/usr/bin")
	envHasNoKey(t, env, "STRIPE_API_KEY")
}

func hasPrefix(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}

	return s[:len(prefix)] == prefix
}

func TestBuildCmdArgs_DoesNotMutateRuntimeExec(t *testing.T) {
	t.Parallel()

	// Regression guard: buildCmdArgs used to `append(execArgs, …)` which
	// could clobber rt.Exec under the hood if cap > len. A repeated call
	// must return the same args slice.
	rt := &executor.Runtime{
		ResolvedConfig: executor.ResolvedConfig{Model: "m", Exec: []string{"serve"}},
	}
	_ = buildCmdArgs(rt, "/root")
	second := buildCmdArgs(rt, "/root")

	want := []string{"serve", "--root", "/root", "--model", "m"}
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("second call contaminated: got %q want %q", second, want)
	}

	if !reflect.DeepEqual(rt.Exec, []string{"serve"}) {
		t.Fatalf("rt.Exec mutated: %q", rt.Exec)
	}
}
