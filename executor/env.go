package executor

import (
	"path/filepath"
	"strings"
)

// EnvOptions configure the locked-down environment that Executor
// implementations hand to spawned runtimes (and through them, every
// tool subprocess).
//
// The shape is intentionally explicit so backends with different
// in-agent path layouts can still call one shared builder:
//
//   - system executor: AgentRoot is the host path of the
//     chroot-style materialised tree; BinDirs is
//     <AgentRoot>/usr/bin (a single entry).
//   - docker executor: AgentRoot is the container-side path
//     ("/workspace"); BinDirs lists the per-BIN image-mount
//     directories ("/opt/bins/ping", "/opt/bins/jq", …).
type EnvOptions struct {
	// AgentRoot is the path the agent considers its root — the
	// directory under which HOME, TMPDIR, etc. live. For system
	// it's the host-side materialised tree path; for docker it's
	// the container-internal "/workspace".
	AgentRoot string

	// BinDirs are joined with ":" to form $PATH. For system
	// there is one entry (<AgentRoot>/usr/bin); for docker there
	// is one entry per BIN image mount.
	BinDirs []string
}

// BuildLockedEnv returns the curated environment the runtime should
// see. The runtime — and every tool subprocess descended from it —
// inherits this set; nothing from the host's $PATH / $HOME /
// $SSH_AUTH_SOCK / $AWS_* leaks through. Callers append
// per-provider credential entries (<PROVIDER>_API_KEY /
// <PROVIDER>_API_BASE) on top of this base.
//
// Keys produced:
//
//   - PATH                — strings.Join(BinDirs, ":")
//   - HOME                — <AgentRoot>/home
//   - XDG_CONFIG_HOME     — <AgentRoot>/home/.config
//   - XDG_CACHE_HOME      — <AgentRoot>/home/.cache
//   - XDG_DATA_HOME       — <AgentRoot>/home/.local/share
//   - TMPDIR              — <AgentRoot>/tmp
//   - LANG                — C.UTF-8
//   - OTTERS_AGENT_ROOT   — <AgentRoot>
func BuildLockedEnv(opts EnvOptions) []string {
	home := filepath.Join(opts.AgentRoot, "home")

	return []string{
		"PATH=" + strings.Join(opts.BinDirs, ":"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"TMPDIR=" + filepath.Join(opts.AgentRoot, "tmp"),
		"LANG=C.UTF-8",
		"OTTERS_AGENT_ROOT=" + opts.AgentRoot,
	}
}
