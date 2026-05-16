package spec

import (
	"fmt"
	"path/filepath"
	"strings"
)

// GenerateAgentMD generates markdown documentation from an Agentfile.
// caps is the list of LLM-facing tool functions the runtime image
// registers (daemon-supplied; the Agentfile itself doesn't declare
// them today). Each entry's description shows up in the "Capabilities"
// section so the model can read what each tool does without invoking
// it.
func GenerateAgentMD(af *Agentfile, caps []Capability) string {
	a := af.Agent
	var b strings.Builder

	fmt.Fprintf(&b, "# %s\n\n", a.Name)

	if desc, ok := a.Labels["description"]; ok {
		b.WriteString(desc + "\n\n")
	}

	writeBinariesSection(&b, a.Bins)

	if len(a.Adds) > 0 {
		b.WriteString("## Data Files\n\n")
		b.WriteString("| File | Description |\n")
		b.WriteString("|------|-------------|\n")

		for _, add := range a.Adds {
			desc := add.Description
			if desc == "" {
				desc = "-"
			}

			fmt.Fprintf(&b, "| %s | %s |\n", filepath.Base(add.Dst), desc)
		}

		b.WriteByte('\n')
	}

	if len(a.Envs) > 0 {
		b.WriteString("## Environment\n\n")
		b.WriteString("| Key | Value | Description |\n")
		b.WriteString("|-----|-------|-------------|\n")

		for _, env := range a.Envs {
			desc := env.Description
			if desc == "" {
				desc = "-"
			}

			fmt.Fprintf(&b, "| `%s` | `%s` | %s |\n", env.Key, env.Value, desc)
		}

		b.WriteByte('\n')
	}

	writeCapabilitiesSection(&b, caps)
	writeMemoryDisciplineSection(&b, caps)

	b.WriteString("## Filesystem\n\n")
	b.WriteString("| Path | Access |\n")
	b.WriteString("|------|--------|\n")
	b.WriteString("| workspace/ | read-write |\n")
	b.WriteString("| tmp/ | read-write |\n")
	b.WriteString("| var/lib/ | read-write |\n")

	return b.String()
}

// writeCapabilitiesSection enumerates the LLM-facing tool functions
// the runtime image registers (each carries its own description). The
// model treats this list as ground truth: any tool name here is
// callable, any name absent is not. Empty list → no section.
//
// The motivating bug: models with no authoritative capability info
// routinely *invent* tools that don't exist or claim missing
// capabilities they actually have. This block is the positive
// counterpart to the Binaries allowlist — both together rule out
// hallucinated tools and hallucinated absences.
func writeCapabilitiesSection(b *strings.Builder, caps []Capability) {
	if len(caps) == 0 {
		return
	}

	b.WriteString("## Capabilities\n\n")
	b.WriteString("These tool functions are **callable** by you. ")
	b.WriteString("Each is registered by the runtime itself (not by an Agentfile BIN directive). ")
	b.WriteString("Treat the list as ground truth: a name here is callable, a name absent is not.\n\n")

	for _, c := range caps {
		desc := c.Description
		if desc == "" {
			desc = "-"
		}
		fmt.Fprintf(b, "- **`%s`** — %s\n", c.Name, desc)
	}

	b.WriteString("\n")
	b.WriteString("If a request needs a capability *not* in this list, say so plainly and stop — ")
	b.WriteString("the same rule as for missing binaries. Do not assume something is missing without ")
	b.WriteString("checking this list first.\n\n")
}

// writeMemoryDisciplineSection emits a "Memory" block that teaches
// the model when to use the note_* tools — gated on the presence of
// note_save in the capability list, so runtimes without the notes
// store don't get a section that points at tools that aren't there.
//
// The capabilities list lists each tool with its own description.
// Without this companion section the model knows the tools EXIST
// but has no rule for WHEN to use them, and tends to forget the
// store across turns. The block below is the operating discipline:
// what to save, when to pin, when to start a task with note_list.
//
// Phrasing is deliberately directive ("save", "start by") rather
// than descriptive — models follow imperatives more reliably than
// "you may consider saving" suggestions.
func writeMemoryDisciplineSection(b *strings.Builder, caps []Capability) {
	if !capsContain(caps, "note_save") {
		return
	}

	b.WriteString("## Memory — when to save, when to read\n\n")
	b.WriteString("The `note_*` capabilities above are your **durable memory across sessions**. ")
	b.WriteString("Chat history is compacted and eventually drops old facts; notes don't. ")
	b.WriteString("Use them like this:\n\n")

	b.WriteString("**Save** (`note_save`) whenever the user states a fact you'll want again:\n")
	b.WriteString("- cluster / project / environment names\n")
	b.WriteString("- paths the user prefers (`/kubeconfig.yaml`, `~/.config/foo`)\n")
	b.WriteString("- conventions the user expects you to follow (units, output format, naming)\n")
	b.WriteString("- credentials *locations* (never the values — env-var name only)\n\n")

	b.WriteString("**Pin** (`note_pin`) a saved note when you'll reference it on every step ")
	b.WriteString("of the current task — a target cluster, an active project, a deployment ")
	b.WriteString("invariant. Pinned notes flow into your system prompt as full-content blocks ")
	b.WriteString("on every step, so you don't burn tool calls re-reading them. ")
	b.WriteString("**Unpin** (`note_unpin`) when that task is done — keep the pinned set tight.\n\n")

	b.WriteString("**Start complex tasks with `note_list`.** Before asking the user a question ")
	b.WriteString("you might already have the answer to, scan the note list. ")
	b.WriteString("`note_show <key>` returns the full body when a preview isn't enough.\n\n")

	b.WriteString("**Don't save:** one-off context (this turn's question), transient state ")
	b.WriteString("(a running job ID), or anything the user has explicitly retracted. ")
	b.WriteString("`note_delete <key>` when a fact stops being true.\n\n")
}

// capsContain reports whether the supplied capability list has a
// tool with the given name. Used by writeMemoryDisciplineSection to
// gate the memory block on note_save presence.
func capsContain(caps []Capability, name string) bool {
	for _, c := range caps {
		if c.Name == name {
			return true
		}
	}
	return false
}

// writeBinariesSection emits the "Binaries" block with an explicit
// allowlist rule: the listed names are the ONLY tools the model may
// call, no shell / no implicit Unix utilities. Without this models
// reliably hallucinate `ls`, `cat`, `grep`, `awk`, etc. — they're
// trained on shell-like contexts and assume those are universally
// available. The "ONLY" phrasing + the explicit forbidden-examples
// line catch this; the closing rule about asking the operator gives
// the model a fallback that isn't "guess and pretend".
//
// Bins with no Usage block still get a description line. An agent
// with zero BINs gets an explicit "no tools available" section
// rather than silently omitting it — silent omission lets the model
// fall back on training-data tools.
func writeBinariesSection(b *strings.Builder, bins []*Bin) {
	b.WriteString("## Binaries\n\n")

	if len(bins) == 0 {
		b.WriteString("This agent has **no BIN tools available**. ")
		b.WriteString("You cannot execute shell commands, scripts, or any external binaries. ")
		b.WriteString("If a request requires running a tool, tell the operator that no tool is wired up ")
		b.WriteString("instead of guessing or pretending to run one.\n\n")
		return
	}

	b.WriteString("**These are the ONLY binaries you may invoke.** ")
	b.WriteString("There is no shell, no PATH lookup beyond this list, and no implicit Unix utilities ")
	b.WriteString("(`ls`, `cat`, `grep`, `awk`, `sed`, `find`, `cp`, `mv`, `rm`, …) unless they appear below. ")
	b.WriteString("Do not assume any command exists just because it's common — ")
	b.WriteString("every tool you can call is in this list and nowhere else.\n\n")

	for _, t := range bins {
		fmt.Fprintf(b, "- **%s**", t.Name)

		if t.Description != "" {
			fmt.Fprintf(b, " — %s", t.Description)
		}

		b.WriteByte('\n')

		if t.Usage != "" {
			for _, line := range strings.Split(t.Usage, "\n") {
				b.WriteString("  " + line + "\n")
			}
		}
	}

	b.WriteByte('\n')
	writeBinaryCallingConvention(b)
	b.WriteString("If you need a capability that isn't covered by the binaries above, ")
	b.WriteString("**say so plainly** — tell the operator which tool would be needed and stop. ")
	b.WriteString("Do not invent a tool call, fabricate output, or substitute a tool from the list ")
	b.WriteString("for one that isn't there.\n\n")
}

// writeBinaryCallingConvention pins the exact JSON shape every BIN call
// must use. The motivating bug: models occasionally emit `args` as a
// JSON-encoded *string* (`"[\"-c\",\"echo hi\"]"`) instead of a real
// JSON array, especially when the payload has nested quotes (jq filters,
// shell one-liners). The runtime's typed unmarshal then rejects the
// call with "invalid parameters" and the turn stalls.
//
// Showing the model both the correct and the wrong form here gives it
// a concrete pattern to imitate. The error case has to be shown
// verbatim — paraphrasing it ("don't stringify") doesn't suppress the
// failure as reliably as a side-by-side example.
func writeBinaryCallingConvention(b *strings.Builder) {
	b.WriteString("### How to call a binary\n\n")
	b.WriteString("Each invocation is a JSON object with two optional fields. ")
	b.WriteString("At least one must be present.\n\n")
	b.WriteString("- **`args`** — array of strings, forwarded as argv. ")
	b.WriteString("Must be a real JSON array, never a JSON-encoded string.\n")
	b.WriteString("- **`stdin`** — string piped to the binary's standard input.\n\n")
	b.WriteString("Correct:   `{\"args\": [\"-c\", \"echo hi\"]}`\n\n")
	b.WriteString("Incorrect: `{\"args\": \"[\\\"-c\\\",\\\"echo hi\\\"]\"}` ")
	b.WriteString("— `args` is a stringified array; the call is rejected with `invalid parameters`.\n\n")
}
