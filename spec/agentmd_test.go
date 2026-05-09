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

	// A minimal Agentfile (no Labels, no Bins, no Adds) still renders header
	// and the always-emitted Filesystem block.
	md := spec.GenerateAgentMD(&spec.Agentfile{Agent: &spec.Agent{Name: "blank"}})

	if !strings.HasPrefix(md, "# blank\n") {
		t.Fatalf("missing header:\n%s", md)
	}

	if strings.Contains(md, "## Binaries") || strings.Contains(md, "## Data Files") || strings.Contains(md, "## Environment") {
		t.Fatalf("minimal file should omit Binaries/Data Files/Environment sections:\n%s", md)
	}

	if !strings.Contains(md, "## Filesystem") {
		t.Fatalf("Filesystem block must always render:\n%s", md)
	}
}
