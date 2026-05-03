//go:build darwin

//nolint:testpackage // direct internal access
package system

import (
	"strings"
	"testing"
)

func TestRenderSBPL_DefaultDeny(t *testing.T) {
	t.Parallel()

	sbpl := renderSBPL(sandboxParams{
		AgentRoot:  "/agents/abc",
		RuntimeBin: "/agents/abc/usr/local/bin/runtime",
	})

	if !strings.HasPrefix(sbpl, "(version 1)\n(deny default)\n") {
		t.Fatalf("profile must start with version 1 + deny default; got:\n%s", sbpl)
	}
}

func TestRenderSBPL_AgentRootRWAndExecBin(t *testing.T) {
	t.Parallel()

	sbpl := renderSBPL(sandboxParams{
		AgentRoot:  "/agents/abc",
		RuntimeBin: "/agents/abc/usr/local/bin/runtime",
	})

	for _, needle := range []string{
		`(allow file-read*  (subpath "/agents/abc"))`,
		`(allow file-write* (subpath "/agents/abc"))`,
		`(allow process-exec (subpath "/agents/abc/usr/bin"))`,
		`(allow process-exec (literal "/agents/abc/usr/local/bin/runtime"))`,
		`(allow file-read*   (literal "/agents/abc/usr/local/bin/runtime"))`,
	} {
		if !strings.Contains(sbpl, needle) {
			t.Errorf("missing %q in profile:\n%s", needle, sbpl)
		}
	}
}

func TestRenderSBPL_NoNetworkByDefault(t *testing.T) {
	t.Parallel()

	sbpl := renderSBPL(sandboxParams{
		AgentRoot:      "/agents/abc",
		RuntimeBin:     "/agents/abc/usr/local/bin/runtime",
		NetworkAllowed: false,
	})

	if strings.Contains(sbpl, "network") {
		t.Fatalf("default profile should not mention network; got:\n%s", sbpl)
	}
}

func TestRenderSBPL_NetworkAllowedAddsRule(t *testing.T) {
	t.Parallel()

	sbpl := renderSBPL(sandboxParams{
		AgentRoot:      "/agents/abc",
		RuntimeBin:     "/agents/abc/usr/local/bin/runtime",
		NetworkAllowed: true,
	})

	if !strings.Contains(sbpl, "(allow network*)") {
		t.Errorf("net-tool agent should grant network*; got:\n%s", sbpl)
	}
}

func TestRenderSBPL_MountsAddPerPathAllow(t *testing.T) {
	t.Parallel()

	sbpl := renderSBPL(sandboxParams{
		AgentRoot:  "/agents/abc",
		RuntimeBin: "/agents/abc/usr/local/bin/runtime",
		Mounts: []sandboxMount{
			{Host: "/Users/me/proj", RW: true},
			{Host: "/Users/me/readonly", RW: false},
		},
	})

	for _, needle := range []string{
		`(allow file-read*  (subpath "/Users/me/proj"))`,
		`(allow file-write* (subpath "/Users/me/proj"))`,
		`(allow file-read*  (subpath "/Users/me/readonly"))`,
	} {
		if !strings.Contains(sbpl, needle) {
			t.Errorf("missing %q in profile:\n%s", needle, sbpl)
		}
	}

	// Read-only mount must NOT get a write rule.
	if strings.Contains(sbpl, `(allow file-write* (subpath "/Users/me/readonly"))`) {
		t.Errorf("ro mount got write rule:\n%s", sbpl)
	}
}

func TestDarwinWrapper_Name(t *testing.T) {
	t.Parallel()

	w := newWrapper(sandboxParams{AgentRoot: "/x", RuntimeBin: "/x/r"})
	if w.Name() != "sandbox-exec" {
		t.Errorf("Name() = %q, want sandbox-exec", w.Name())
	}
}

func TestDarwinWrapper_PrependsSandboxExec(t *testing.T) {
	t.Parallel()

	w := newWrapper(sandboxParams{AgentRoot: "/x", RuntimeBin: "/x/r"})
	out := w.Wrap([]string{"/x/r", "serve", "--root", "/x"})

	if len(out) < 4 || out[0] != "sandbox-exec" || out[1] != "-p" {
		t.Fatalf("wrap argv = %v; want sandbox-exec -p <profile> ...", out)
	}
	// argv tail must preserve the original command.
	if out[3] != "/x/r" || out[4] != "serve" {
		t.Fatalf("wrap argv tail = %v; want /x/r serve at positions 3..4", out)
	}
}

func TestWrapperKind_Darwin(t *testing.T) {
	t.Parallel()

	if got := wrapperKind(); got != "sandbox-exec" {
		t.Errorf("wrapperKind() = %q, want sandbox-exec", got)
	}
}
