package system

import (
	"github.com/openotters/agentfile/agent"
)

// Sandbox impl names. Stamped into WORKSPACE.md and reported by
// each wrapper's Name() so downstream code can branch on which
// isolation primitive is in effect.
const (
	sandboxKindDarwin = "sandbox-exec"
	sandboxKindLinux  = "bwrap"
	sandboxKindNone   = "none"
)

// sandboxParams describe what an agent's per-OS sandbox needs to
// allow. Built once per agent at runtime spawn from the agent's own
// metadata; never mutated after.
type sandboxParams struct {
	// AgentRoot is the absolute path to the agent's chroot-style
	// directory on the host (e.g. ~/.otters/agents/<id>). Reads /
	// writes inside this tree are always allowed.
	AgentRoot string

	// RuntimeBin is the absolute path to the runtime binary the
	// sandboxed process needs to exec. Lives outside the agent
	// root, so it gets its own allow rule.
	RuntimeBin string

	// Mounts are the host paths the user passed via -v. Each gets
	// a per-mount allow block.
	Mounts []sandboxMount

	// NetworkAllowed is the OR over the agent's BIN tools of
	// "needs network" — true when any net-tool BIN is present
	// (jina/wget/curl/ping). All-or-nothing per agent because
	// inheritance is the cheap-fork-wrap trick that lets a single
	// profile cover the runtime + every tool subprocess.
	NetworkAllowed bool
}

type sandboxMount struct {
	Host string
	RW   bool
}

// netToolNames is the closed set of BIN aliases that the daemon /
// agentfile build pipeline recognises as network-using. Adding a
// tool that needs network and isn't in this set means it'll be
// blocked by the sandbox; expand the set rather than disabling the
// sandbox.
var netToolNames = map[string]struct{}{ //nolint:gochecknoglobals // immutable lookup table
	"jina": {},
	"wget": {},
	"curl": {},
	"ping": {},
}

// sandboxParamsFor builds the per-agent params from runtime metadata,
// the resolved mount list, and the agent root.
func sandboxParamsFor(rt *agent.AgentRuntime, mounts []Mount, agentRoot, runtimeBin string) sandboxParams {
	out := sandboxParams{
		AgentRoot:  agentRoot,
		RuntimeBin: runtimeBin,
		Mounts:     make([]sandboxMount, 0, len(mounts)),
	}

	for _, m := range mounts {
		// Mounts are bidirectional today (no read-only flag in the
		// public API). Treat all as RW until / unless the API gains
		// a flag.
		out.Mounts = append(out.Mounts, sandboxMount{Host: m.Host, RW: true})
	}

	if rt != nil {
		for _, t := range rt.Tools {
			if _, ok := netToolNames[t.Name]; ok {
				out.NetworkAllowed = true
				break
			}
		}
	}

	return out
}

// wrapper produces argv that wraps the runtime spawn in a per-OS
// sandbox primitive. Identity (no-op) on unsupported platforms or
// when the platform's sandbox tool is missing (e.g. bwrap not
// installed on Linux).
type wrapper interface {
	// Wrap returns the new argv with the sandbox primitive prepended.
	// argv[0] is the binary the caller wants to launch; the wrapper
	// returns ["sandbox-exec", "-p", profile, argv...] on macOS,
	// ["bwrap", flags..., "--", argv...] on Linux, or argv unchanged
	// on the no-op path.
	Wrap(argv []string) []string

	// Name identifies the wrapper for logs and Workspace.md
	// rendering. One of "sandbox-exec", "bwrap", "none".
	Name() string
}

// noopWrapper is the identity wrap. Used on Windows / BSD and as
// the bwrap fallback when bubblewrap isn't installed.
type noopWrapper struct{}

func (noopWrapper) Wrap(argv []string) []string { return argv }
func (noopWrapper) Name() string                { return sandboxKindNone }

// sandboxFor builds the wrapper for an agent. Indirected through a
// package-level var so tests in this package can substitute a no-op
// wrapper without recompiling for a different GOOS — the production
// per-OS impls are still selected by build tag at compile time.
//
//nolint:gochecknoglobals // function-valued, test seam
var sandboxFor = newWrapper
