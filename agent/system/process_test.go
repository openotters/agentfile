//nolint:testpackage // direct internal access
package system

import (
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/openotters/agentfile/agent"
)

func TestBuildCmdArgs_DefaultsToServe(t *testing.T) {
	t.Parallel()

	got := buildCmdArgs(&agent.AgentRuntime{ResolvedConfig: agent.ResolvedConfig{Model: "anthropic/claude"}}, "/root")

	want := []string{"serve", "--root", "/root", "--model", "anthropic/claude"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestBuildCmdArgs_CustomExecVerb(t *testing.T) {
	t.Parallel()

	got := buildCmdArgs(&agent.AgentRuntime{
		ResolvedConfig: agent.ResolvedConfig{
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
	got := buildCmdArgs(&agent.AgentRuntime{
		ResolvedConfig: agent.ResolvedConfig{
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

	got := buildCmdArgs(&agent.AgentRuntime{
		ResolvedConfig: agent.ResolvedConfig{Model: "m", Addr: "127.0.0.1:9999"},
	}, "/root")

	want := []string{"serve", "--root", "/root", "--model", "m", "--addr", "127.0.0.1:9999"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestBuildCmdArgs_ExtraArgsAppended(t *testing.T) {
	t.Parallel()

	got := buildCmdArgs(&agent.AgentRuntime{
		ResolvedConfig: agent.ResolvedConfig{Model: "m"},
	}, "/root", "--debug", "msg")

	want := []string{"serve", "--root", "/root", "--model", "m", "--debug", "msg"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestBuildCmdArgs_FullComposition(t *testing.T) {
	t.Parallel()

	// Exec + base flags + addr + extras — credentials are excluded by
	// design (see TestBuildCmdArgs_DoesNotEmitCredentialFlags); they
	// flow through buildCmdEnv instead.
	got := buildCmdArgs(&agent.AgentRuntime{
		ResolvedConfig: agent.ResolvedConfig{
			Model:   "openai/gpt-4",
			APIBase: "https://api.openai.com",
			APIKey:  "sk-xxx",
			Addr:    "127.0.0.1:1234",
			Exec:    []string{"serve"},
		},
	}, "/root", "--trace")

	want := []string{
		"serve",
		"--root", "/root",
		"--model", "openai/gpt-4",
		"--addr", "127.0.0.1:1234",
		"--trace",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// envDiff returns the entries that buildCmdEnv added on top of
// os.Environ. The host's own ANTHROPIC_API_KEY (or whatever) is in the
// baseline; we only care about what buildCmdEnv contributes. Asserting
// against the diff keeps the tests stable regardless of the developer's
// shell.
func envDiff(t *testing.T, env []string) []string {
	t.Helper()

	baseline := make(map[string]struct{}, len(os.Environ()))
	for _, e := range os.Environ() {
		baseline[e] = struct{}{}
	}

	diff := make([]string, 0)

	for _, e := range env {
		if _, present := baseline[e]; !present {
			diff = append(diff, e)
		}
	}

	return diff
}

func TestBuildCmdEnv_EmitsProviderCredentials(t *testing.T) {
	t.Parallel()

	rt := &agent.AgentRuntime{
		ResolvedConfig: agent.ResolvedConfig{
			Model:   "anthropic/claude-sonnet-4-5",
			APIBase: "https://api.anthropic.com",
			APIKey:  "sk-test-fixture-not-real",
		},
	}

	env := buildCmdEnv(rt)
	diff := envDiff(t, env)

	want := []string{
		"ANTHROPIC_API_KEY=sk-test-fixture-not-real",
		"ANTHROPIC_API_BASE=https://api.anthropic.com",
	}
	for _, w := range want {
		if !slices.Contains(diff, w) {
			t.Errorf("envDiff missing %q; got %v", w, diff)
		}
	}

	// Inheritance check: env must be ≥ os.Environ in length.
	if len(env) < len(os.Environ()) {
		t.Errorf("buildCmdEnv truncated host env: got %d entries, host has %d",
			len(env), len(os.Environ()))
	}
}

func TestBuildCmdEnv_OmitsAbsentFields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		rt          *agent.AgentRuntime
		mustNotHave []string
	}{
		{
			name:        "no key",
			rt:          &agent.AgentRuntime{ResolvedConfig: agent.ResolvedConfig{Model: "anthropic/m", APIBase: "https://x"}},
			mustNotHave: []string{"ANTHROPIC_API_KEY="},
		},
		{
			name:        "no base",
			rt:          &agent.AgentRuntime{ResolvedConfig: agent.ResolvedConfig{Model: "anthropic/m", APIKey: "k"}},
			mustNotHave: []string{"ANTHROPIC_API_BASE="},
		},
		{
			name:        "neither",
			rt:          &agent.AgentRuntime{ResolvedConfig: agent.ResolvedConfig{Model: "anthropic/m"}},
			mustNotHave: []string{"ANTHROPIC_API_KEY=", "ANTHROPIC_API_BASE="},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			diff := envDiff(t, buildCmdEnv(tc.rt))

			for _, prefix := range tc.mustNotHave {
				for _, e := range diff {
					if hasPrefix(e, prefix) {
						t.Errorf("envDiff added %q; expected nothing matching %q", e, prefix)
					}
				}
			}
		})
	}
}

func TestBuildCmdEnv_NoProviderInModel(t *testing.T) {
	t.Parallel()

	// A bare model name (no "provider/") yields no added entries —
	// the provider prefix is what scopes the env names.
	diff := envDiff(t, buildCmdEnv(&agent.AgentRuntime{
		ResolvedConfig: agent.ResolvedConfig{
			Model:   "bare-name",
			APIKey:  "k",
			APIBase: "https://x",
		},
	}))

	if len(diff) != 0 {
		t.Errorf("envDiff = %v, want empty", diff)
	}
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
	rt := &agent.AgentRuntime{
		ResolvedConfig: agent.ResolvedConfig{Model: "m", Exec: []string{"serve"}},
	}
	_ = buildCmdArgs(rt, "/root", "--first")
	second := buildCmdArgs(rt, "/root", "--second")

	want := []string{"serve", "--root", "/root", "--model", "m", "--second"}
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("second call contaminated: got %q want %q", second, want)
	}

	if !reflect.DeepEqual(rt.Exec, []string{"serve"}) {
		t.Fatalf("rt.Exec mutated: %q", rt.Exec)
	}
}
