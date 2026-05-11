package spec_test

import (
	"strings"
	"testing"

	"github.com/openotters/agentfile/spec"
)

func TestGenerateAgentMD(t *testing.T) {
	t.Parallel()

	af := &spec.Agentfile{Agent: &spec.Agent{
		Name:   "meteo",
		Labels: map[string]string{"description": "weather bot"},
		Bins: []*spec.Bin{
			{Name: "wget", Description: "Fetch URLs", Usage: "wget <url>"},
			{Name: "jq"},
		},
		Adds: []*spec.Add{
			{Dst: "/data/cities.json", Description: "city coords"},
			{Dst: "/data/flags.bin"},
		},
		Envs: []*spec.Env{
			{Key: "NODE_ENV", Value: "production", Description: "App env"},
			{Key: "FEATURE_X", Value: "on"},
		},
	}}

	md := spec.GenerateAgentMD(af)

	// Header comes from Agent.Name.
	if !strings.HasPrefix(md, "# meteo\n") {
		t.Fatalf("missing header:\n%s", md)
	}

	// Description from Labels.
	if !strings.Contains(md, "weather bot") {
		t.Fatalf("missing description from Labels:\n%s", md)
	}

	// Binaries list.
	for _, needle := range []string{"## Binaries", "**wget**", "Fetch URLs", "**jq**", "wget <url>"} {
		if !strings.Contains(md, needle) {
			t.Fatalf("missing %q in output:\n%s", needle, md)
		}
	}

	// Data files table — rendered with basename(Dst), and a "-" when no Description.
	for _, needle := range []string{"## Data Files", "cities.json", "city coords", "flags.bin", "| - |"} {
		if !strings.Contains(md, needle) {
			t.Fatalf("missing %q in output:\n%s", needle, md)
		}
	}

	// Environment table — rendered with backticked key/value, "-" when no Description.
	for _, needle := range []string{"## Environment", "`NODE_ENV`", "`production`", "App env", "`FEATURE_X`", "`on`"} {
		if !strings.Contains(md, needle) {
			t.Fatalf("missing %q in output:\n%s", needle, md)
		}
	}

	// Filesystem block is always emitted.
	if !strings.Contains(md, "## Filesystem") || !strings.Contains(md, "workspace/") {
		t.Fatalf("missing Filesystem block:\n%s", md)
	}
}

func TestGenerateAgentMD_Minimal(t *testing.T) {
	t.Parallel()

	// A minimal Agentfile (no Labels, no Bins, no Adds) still renders
	// the header, the Filesystem block, AND a Binaries section
	// containing an explicit "no tools available" rule. Empty BINs
	// would otherwise let the model fall back on training-data shell
	// utilities (`ls`, `cat`, …) and pretend to invoke them.
	md := spec.GenerateAgentMD(&spec.Agentfile{Agent: &spec.Agent{Name: "blank"}})

	if !strings.HasPrefix(md, "# blank\n") {
		t.Fatalf("missing header:\n%s", md)
	}

	// Data Files / Environment are still conditional on actual entries.
	for _, section := range []string{"## Data Files", "## Environment"} {
		if strings.Contains(md, section) {
			t.Fatalf("minimal file should omit %s section:\n%s", section, md)
		}
	}

	// Binaries section IS rendered even when empty, with an explicit
	// allowlist-rule statement so the model can't drift into
	// hallucinated shell commands.
	for _, needle := range []string{"## Binaries", "no BIN tools available"} {
		if !strings.Contains(md, needle) {
			t.Fatalf("missing %q in minimal output:\n%s", needle, md)
		}
	}

	if !strings.Contains(md, "## Filesystem") {
		t.Fatalf("Filesystem block must always render:\n%s", md)
	}
}

func TestGenerateAgentMD_DeclaresCapabilities(t *testing.T) {
	t.Parallel()

	// Every AGENT.md must enumerate the positive capabilities the
	// runtime actually has. The motivating bug: models reply "yaegi
	// is a sandbox with no network access" — a hallucinated
	// constraint — when the docker container in fact has full
	// egress. The Capabilities section is the runtime's ground
	// truth; this test pins the load-bearing facts so a refactor
	// can't silently drop one and re-open the bug.
	md := spec.GenerateAgentMD(&spec.Agentfile{Agent: &spec.Agent{Name: "any"}})

	for _, needle := range []string{
		"## Capabilities",
		"Outbound network",
		"Persistent memory",
		"Async jobs",
		"Persistent workspace",
		// Anti-hallucination phrasing — without this the section
		// reads as a brochure, not a directive.
		"Do not tell the operator a capability is missing",
	} {
		if !strings.Contains(md, needle) {
			t.Fatalf("missing capability phrase %q in output:\n%s", needle, md)
		}
	}
}

func TestGenerateAgentMD_EnforcesAllowlistRule(t *testing.T) {
	t.Parallel()

	// Whenever BINs are declared, the Binaries section must lead
	// with the "ONLY binaries you may invoke" allowlist phrasing.
	// Without it, models reliably hallucinate `ls`/`cat`/`grep` even
	// when those tools are absent. Tightening or replacing this
	// phrasing requires updating this test deliberately — silent
	// drift would re-open the hallucination class of bugs.
	md := spec.GenerateAgentMD(&spec.Agentfile{Agent: &spec.Agent{
		Name: "demo",
		Bins: []*spec.Bin{{Name: "jq"}},
	}})

	for _, needle := range []string{
		"ONLY binaries you may invoke",
		"no implicit Unix utilities",
		"say so plainly",
	} {
		if !strings.Contains(md, needle) {
			t.Fatalf("missing allowlist-rule phrase %q in output:\n%s", needle, md)
		}
	}
}
