package executor

import (
	"path/filepath"
	"strings"

	"github.com/openotters/agentfile/spec"
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

// reservedRuntimeEnvKeys are the keys produced by BuildLockedEnv.
// AppendUserEnv refuses to overwrite them — overriding any of these
// breaks the sandbox guarantees (PATH points at the agent's BIN
// dirs, HOME / XDG_* / TMPDIR / OTTERS_AGENT_ROOT anchor the
// materialised tree). spec.Validate already rejects user envs with
// these keys at build time; the runtime check here is a belt-and-
// braces filter for malformed agent.yaml.
//
//nolint:gochecknoglobals // immutable allowlist consulted by AppendUserEnv
var reservedRuntimeEnvKeys = map[string]struct{}{
	"PATH":              {},
	"HOME":              {},
	"XDG_CONFIG_HOME":   {},
	"XDG_CACHE_HOME":    {},
	"XDG_DATA_HOME":     {},
	"TMPDIR":            {},
	"LANG":              {},
	"OTTERS_AGENT_ROOT": {},
}

// AppendUserEnv appends user-declared envs onto a base built by
// BuildLockedEnv (and any provider-cred entries the caller has
// already added). Entries are appended in declaration order; later
// duplicates win, matching os/exec's "last one wins" semantics.
//
// Reserved keys (the locked-env keys, plus *_API_KEY / *_API_BASE
// suffixes used for provider creds) are filtered defensively. The
// returned skipped slice carries the offending keys so the caller
// can log them once. spec.Validate rejects these at build time;
// runtime filtering here is a safety net for malformed agent.yaml.
func AppendUserEnv(base []string, userEnvs []*spec.Env) ([]string, []string) {
	if len(userEnvs) == 0 {
		return base, nil
	}

	env := base

	var skipped []string

	for _, e := range userEnvs {
		if e == nil || e.Key == "" {
			continue
		}

		if _, reserved := reservedRuntimeEnvKeys[e.Key]; reserved {
			skipped = append(skipped, e.Key)

			continue
		}

		if strings.HasSuffix(e.Key, "_API_KEY") || strings.HasSuffix(e.Key, "_API_BASE") {
			skipped = append(skipped, e.Key)

			continue
		}

		env = append(env, e.Key+"="+e.Value)
	}

	return env, skipped
}
