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
			{Name: "cities.json", Description: "city coords"},
			{Name: "flags.bin"},
		},
		Envs: []*spec.Env{
			{Key: "NODE_ENV", Value: "production", Description: "App env"},
			{Key: "FEATURE_X", Value: "on"},
		},
	}}

	md := spec.GenerateAgentMD(af, "", nil)

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
	md := spec.GenerateAgentMD(&spec.Agentfile{Agent: &spec.Agent{Name: "blank"}}, "", nil)

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

	// The calling-convention block lives inside the BIN list and
	// is meaningless when no binaries are wired up — it must NOT
	// render here. Re-emitting it with zero tools would invite the
	// model to call something that doesn't exist.
	if strings.Contains(md, "How to call a binary") {
		t.Fatalf("calling-convention block leaked into zero-BIN output:\n%s", md)
	}

	if !strings.Contains(md, "## Filesystem") {
		t.Fatalf("Filesystem block must always render:\n%s", md)
	}
}

func TestGenerateAgentMD_MemoryDiscipline_PresentWhenNoteSaveCapExists(t *testing.T) {
	t.Parallel()

	// When note_save is in the capability list, AGENT.md MUST render
	// the Memory section that teaches the model when to use the
	// note_* tools. Just listing the capability names doesn't tell
	// the model WHEN to reach for them; the section pins the
	// operating rules (save user-stated facts, pin for the current
	// task, start with note_list, don't save transient state).
	caps := []spec.Capability{
		{Name: "note_save", Description: "Save a durable fact under a key."},
		{Name: "note_list", Description: "List stored notes."},
		{Name: "note_pin", Description: "Pin a note into the prompt."},
	}
	md := spec.GenerateAgentMD(&spec.Agentfile{Agent: &spec.Agent{Name: "any"}}, "", caps)

	for _, needle := range []string{
		"## Memory — when to save, when to read",
		"durable memory across sessions",
		// "Save" / "Pin" / "Start" imperatives — these are
		// load-bearing; switching them back to "you may" /
		// "consider" softer phrasings degrades how reliably the
		// model picks the tool up. Test pins each.
		"**Save**",
		"**Pin**",
		"Start complex tasks with `note_list`",
		// Anti-noise rule — explicit list of things NOT to save.
		// Without this models tend to over-save (transient state,
		// one-off questions) and the store gets noisy.
		"**Don't save:**",
	} {
		if !strings.Contains(md, needle) {
			t.Fatalf("missing %q in output:\n%s", needle, md)
		}
	}
}

func TestGenerateAgentMD_MemoryDiscipline_AbsentWithoutNoteSaveCap(t *testing.T) {
	t.Parallel()

	// Runtimes that don't register the note_* tools (custom forks,
	// alpha environments before the notes feature landed) must NOT
	// see the Memory section — it would point at capabilities that
	// don't exist. Gate is presence of note_save in the cap list.
	caps := []spec.Capability{
		{Name: "context_list", Description: "List context files."},
	}
	md := spec.GenerateAgentMD(&spec.Agentfile{Agent: &spec.Agent{Name: "any"}}, "", caps)

	if strings.Contains(md, "## Memory") {
		t.Fatalf("Memory section leaked when note_save cap is absent:\n%s", md)
	}
}

func TestGenerateAgentMD_DeclaresCapabilities(t *testing.T) {
	t.Parallel()

	// Every AGENT.md with a non-empty capabilities list must render
	// the runtime tools by name with their descriptions. The
	// motivating bug: models reliably hallucinate either missing
	// tools they actually have or invent ones they don't. The
	// Capabilities section is the runtime's ground truth — a name
	// here is callable, a name absent is not. This test pins the
	// load-bearing facts so a refactor can't silently drop one and
	// re-open the hallucination class.
	caps := []spec.Capability{
		{Name: "job_submit", Description: "Submit a BIN as an async job."},
		{Name: "context_show", Description: "Show the content of one context file."},
	}
	md := spec.GenerateAgentMD(&spec.Agentfile{Agent: &spec.Agent{Name: "any"}}, "", caps)

	for _, needle := range []string{
		"## Capabilities",
		"`job_submit`",
		"Submit a BIN as an async job",
		"`context_show`",
		"Show the content of one context file",
		// Anti-hallucination phrasing — without this the section
		// reads as a brochure, not a directive.
		"a name here is callable, a name absent is not",
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
	}}, "", nil)

	for _, needle := range []string{
		"ONLY binaries you may invoke",
		"no implicit Unix utilities",
		"say so plainly",
		// Calling-convention sub-section: explicit good/bad
		// example for the JSON shape. Pins the failure mode
		// where models emit `args` as a stringified array
		// instead of a real JSON array — the side-by-side
		// example is what suppresses the bug.
		"How to call a binary",
		"`args`",
		"`stdin`",
		"never a JSON-encoded string",
		"{\"args\": [\"-c\", \"echo hi\"]}",
		"{\"args\": \"[\\\"-c\\\",\\\"echo hi\\\"]\"}",
		"invalid parameters",
	} {
		if !strings.Contains(md, needle) {
			t.Fatalf("missing allowlist-rule phrase %q in output:\n%s", needle, md)
		}
	}
}
