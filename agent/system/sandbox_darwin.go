//go:build darwin

package system

import (
	"fmt"
	"strings"
)

// wrapperKind identifies the sandbox impl used on this platform.
// Stamped into WORKSPACE.md so the agent knows what isolation it has.
// Always "sandbox-exec" on macOS — the binary is part of the base
// system since Mac OS X 10.5.
func wrapperKind() string { return sandboxKindDarwin }

// newWrapper returns a sandbox-exec wrapper for the supplied params.
// The SBPL profile is rendered once and held on the wrapper so each
// Wrap call is just a slice prepend.
func newWrapper(p sandboxParams) wrapper {
	return &darwinWrapper{profile: renderSBPL(p)}
}

type darwinWrapper struct {
	profile string
}

func (d *darwinWrapper) Wrap(argv []string) []string {
	return append([]string{"sandbox-exec", "-p", d.profile}, argv...)
}

func (d *darwinWrapper) Name() string { return sandboxKindDarwin }

// renderSBPL produces the per-agent macOS sandbox profile in Apple's
// Sandbox Profile Language. The shape is documented in the plan
// (workspace/openotters plan file) and pinned by golden tests.
//
// Layers:
//
//  1. Default deny.
//  2. Agent-root read+write + exec from <root>/usr/bin.
//  3. Runtime binary itself (lives outside the agent root).
//  4. Minimal system reads tools genuinely need (libc, dyld, locale,
//     /etc/services, /etc/protocols, /etc/passwd).
//  5. Process / signal / sysctl primitives.
//  6. Mach IPC stubs needed by libSystem.
//  7. Per-mount read+write allow blocks.
//  8. Network — only when the agent has any net-tool BIN.
func renderSBPL(p sandboxParams) string {
	var b strings.Builder

	b.WriteString("(version 1)\n")
	b.WriteString("(deny default)\n\n")

	fmt.Fprintf(&b, "(allow file-read*  (subpath %q))\n", p.AgentRoot)
	fmt.Fprintf(&b, "(allow file-write* (subpath %q))\n", p.AgentRoot)
	fmt.Fprintf(&b, "(allow process-exec (subpath %q))\n\n",
		p.AgentRoot+"/usr/bin")

	if p.RuntimeBin != "" {
		fmt.Fprintf(&b, "(allow process-exec (literal %q))\n", p.RuntimeBin)
		fmt.Fprintf(&b, "(allow file-read*   (literal %q))\n\n", p.RuntimeBin)
	}

	// Minimal system reads.
	b.WriteString("(allow file-read*\n")
	b.WriteString("  (literal \"/etc/services\") (literal \"/etc/protocols\")\n")
	b.WriteString("  (literal \"/etc/passwd\") (literal \"/etc/hosts\")\n")
	b.WriteString("  (literal \"/etc/resolv.conf\")\n")
	b.WriteString("  (subpath \"/usr/lib\") (subpath \"/System/Library\")\n")
	b.WriteString("  (subpath \"/usr/share/zoneinfo\")\n")
	b.WriteString("  (subpath \"/private/var/db/timezone\"))\n\n")

	b.WriteString("(allow process-fork)\n")
	b.WriteString("(allow signal (target self))\n")
	b.WriteString("(allow sysctl-read)\n\n")

	b.WriteString("(allow mach-lookup\n")
	b.WriteString("  (global-name \"com.apple.system.notification_center\")\n")
	b.WriteString("  (global-name \"com.apple.system.opendirectoryd.libinfo\")\n")
	b.WriteString("  (global-name \"com.apple.system.logger\"))\n\n")

	for _, m := range p.Mounts {
		fmt.Fprintf(&b, "(allow file-read*  (subpath %q))\n", m.Host)
		if m.RW {
			fmt.Fprintf(&b, "(allow file-write* (subpath %q))\n", m.Host)
		}
	}

	if len(p.Mounts) > 0 {
		b.WriteString("\n")
	}

	if p.NetworkAllowed {
		b.WriteString("(allow network*)\n")
	}

	return b.String()
}
