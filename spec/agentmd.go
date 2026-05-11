package spec

import (
	"fmt"
	"path/filepath"
	"strings"
)

// GenerateAgentMD generates markdown documentation from an Agentfile.
func GenerateAgentMD(af *Agentfile) string {
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

	writeCapabilitiesSection(&b)

	b.WriteString("## Filesystem\n\n")
	b.WriteString("| Path | Access |\n")
	b.WriteString("|------|--------|\n")
	b.WriteString("| workspace/ | read-write |\n")
	b.WriteString("| tmp/ | read-write |\n")
	b.WriteString("| var/lib/ | read-write |\n")

	return b.String()
}

// writeCapabilitiesSection enumerates the positive facts about what
// this runtime can actually do. The motivating bug: models with no
// authoritative capability info routinely *invent* constraints
// ("yaegi is a sandbox with no network access") that don't exist.
// Negative-only documentation (the Binaries allowlist above) keeps
// the model from hallucinating tools but does nothing to stop it
// from hallucinating *missing* capabilities. This block is the
// counterpart: a short list of things the runtime DOES support, with
// "do not contradict this" phrasing so the model treats the list as
// ground truth rather than a starting hypothesis.
//
// Every capability listed here holds for both executor backends
// (system + docker) as of the runtime this AGENT.md was written for.
// If a future backend disables one of these (e.g. an air-gapped
// network-disabled mode), it should be declared per-agent via an
// Agentfile-level knob; today the runtime is uniform so the block
// is unconditional.
func writeCapabilitiesSection(b *strings.Builder) {
	b.WriteString("## Capabilities\n\n")
	b.WriteString("These capabilities are **available** to you. ")
	b.WriteString("Do not tell the operator a capability is missing unless it is explicitly absent ")
	b.WriteString("from this list — assume *yes* by default for anything enumerated here, and treat ")
	b.WriteString("absence as a real constraint only when nothing here covers it.\n\n")

	b.WriteString("- **Outbound network** — full HTTP / HTTPS / arbitrary-TCP egress to the public internet. ")
	b.WriteString("Tools that fetch URLs, hit APIs, or open sockets work; no proxy interception, no allowlist. ")
	b.WriteString("(Inbound network is not exposed — you can call out, others cannot call in.)\n")
	b.WriteString("- **Persistent memory** — your conversation history survives session resumption and ")
	b.WriteString("daemon restarts; treat earlier turns as durable, not in-RAM.\n")
	b.WriteString("- **Async jobs** — `job_submit` / `job_wait` / `job_status` / `job_cancel` are wired ")
	b.WriteString("when the daemon URL + token are present in your env. Long-running BIN invocations ")
	b.WriteString("(builds, deploys, queries) should go through these rather than blocking inline ")
	b.WriteString("on a synchronous tool call.\n")
	b.WriteString("- **Persistent workspace** — `workspace/` and `var/lib/` survive across runs of this ")
	b.WriteString("agent; `tmp/` is wiped each restart. Use the persistent paths for state you want back.\n")
	b.WriteString("\n")

	b.WriteString("If a request needs a capability *not* in this list, say so plainly and stop — ")
	b.WriteString("the same rule as for missing binaries. Do not assume something is missing without ")
	b.WriteString("checking this list first.\n\n")
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
	b.WriteString("If you need a capability that isn't covered by the binaries above, ")
	b.WriteString("**say so plainly** — tell the operator which tool would be needed and stop. ")
	b.WriteString("Do not invent a tool call, fabricate output, or substitute a tool from the list ")
	b.WriteString("for one that isn't there.\n\n")
}
