//nolint:testpackage // direct internal access
package system

import (
	"reflect"
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

func TestBuildCmdArgs_APIFlagsOnlyWhenSet(t *testing.T) {
	t.Parallel()

	// APIBase set, APIKey empty → only --api-base is emitted.
	got := buildCmdArgs(&agent.AgentRuntime{
		ResolvedConfig: agent.ResolvedConfig{
			Model:   "m",
			APIBase: "https://api.example.com",
		},
	}, "/root")

	want := []string{"serve", "--root", "/root", "--model", "m", "--api-base", "https://api.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
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

	// Exec + base flags + api-* + addr + extras — asserts the documented
	// ordering contract: exec → --root/--model → api-* (when set) →
	// --addr (when set) → caller extras.
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
		"--api-base", "https://api.openai.com",
		"--api-key", "sk-xxx",
		"--addr", "127.0.0.1:1234",
		"--trace",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
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
