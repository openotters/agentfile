//go:build linux

package system

import (
	"os/exec"
	"path/filepath"
)

// wrapperKind reports whether bwrap is available. Resolved once at
// Provider construction. Returns "bwrap" when the binary is on
// $PATH; "none" when bubblewrap isn't installed (the operator
// should see a one-time warning then; agents fall back to running
// unsandboxed but with the locked env still in effect).
func wrapperKind() string {
	if _, err := bwrapLookPath(); err != nil {
		return sandboxKindNone
	}
	return sandboxKindLinux
}

// newWrapper returns a bwrap wrapper for the supplied params, or
// the no-op wrapper if `bwrap` isn't on $PATH. The fallback is
// intentional: agents must keep working on hosts that haven't
// installed bubblewrap; the operator should see one warning at
// Provider construction.
func newWrapper(p sandboxParams) wrapper {
	bwrapPath, err := bwrapLookPath()
	if err != nil {
		return noopWrapper{}
	}

	return &linuxWrapper{
		bwrapPath: bwrapPath,
		flags:     buildBwrapFlags(p),
	}
}

// bwrapLookPath is indirected so tests can simulate "bwrap not
// installed" without mutating the test process's PATH.
//
//nolint:gochecknoglobals // function-valued, test seam
var bwrapLookPath = func() (string, error) { return exec.LookPath("bwrap") }

type linuxWrapper struct {
	bwrapPath string
	flags     []string
}

func (l *linuxWrapper) Wrap(argv []string) []string {
	out := make([]string, 0, 1+len(l.flags)+1+len(argv))
	out = append(out, l.bwrapPath)
	out = append(out, l.flags...)
	out = append(out, "--")
	out = append(out, argv...)

	return out
}

func (l *linuxWrapper) Name() string { return sandboxKindLinux }

// buildBwrapFlags renders the per-agent bwrap argv (flags only;
// the wrapped command argv is appended separately at Wrap time).
//
// Strategy is "permissive root, narrow agent": mount the host root
// read-only so libc / dyld / locale just work, then bind the agent
// root rw for state, plus per-mount binds for user paths. Network
// is opt-in via --share-net; missing it leaves the namespace's
// loopback-only network in place.
//
// Hardening pass (drop --ro-bind / / and enumerate the real
// minimum bind set: /usr/lib, /etc/{passwd,resolv.conf,services},
// /proc, /dev/{null,urandom}) is deliberately a follow-up — see
// the plan.
func buildBwrapFlags(p sandboxParams) []string {
	flags := []string{
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--unshare-user-try",
		"--die-with-parent",
		// Permissive base: read the host root, keep /tmp / /proc /
		// /dev as the namespace's own.
		"--ro-bind", "/", "/",
		"--proc", "/proc",
		"--dev", "/dev",
		// Agent root is mutable inside the sandbox.
		"--bind", p.AgentRoot, p.AgentRoot,
		// Runtime binary lives outside the agent root.
		"--ro-bind", p.RuntimeBin, p.RuntimeBin,
		// Replace /tmp with a fresh tmpfs to prevent leaking
		// host /tmp content back to the agent.
		"--tmpfs", "/tmp",
		// CWD inside the namespace.
		"--chdir", filepath.Join(p.AgentRoot, "workspace"),
	}

	for _, m := range p.Mounts {
		if m.RW {
			flags = append(flags, "--bind", m.Host, m.Host)
		} else {
			flags = append(flags, "--ro-bind", m.Host, m.Host)
		}
	}

	if !p.NetworkAllowed {
		flags = append(flags, "--unshare-net")
	}

	return flags
}

// fmt is imported for future profile-error formatting helpers.
