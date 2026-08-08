package executor

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/openotters/agentfile/spec"
)

// configEnvPrefix is the env-var prefix produced by AppendConfigEnv.
// Every CONFIG key declared in an Agentfile (or set in agent.yaml's
// configs: block) gets exported as RUNTIME_<UPPER_SNAKE_CASE> on
// the runtime process so tooling that prefers env over config-file
// reads sees the same values without parsing agent.yaml.
const configEnvPrefix = "RUNTIME_"

// configKeyToEnv rewrites a kebab-case CONFIG key into the env-var
// form: lowercase → uppercase, '-' → '_', then RUNTIME_ prefix.
// Agentfile-declared keys are DNS-1123 labels (spec.Validate), so for
// them the rewrite is collision-free; deploy-time overrides
// (WithConfig) are not re-validated, so any non-alphanumeric character
// that arrives that way is replaced with '_' to keep the result a
// legal POSIX identifier.
func configKeyToEnv(key string) string {
	var b strings.Builder
	b.Grow(len(configEnvPrefix) + len(key))
	b.WriteString(configEnvPrefix)
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

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

	// DaemonURL is the openotters daemon's endpoint
	// (unix://<path> or http://<host>:<port>) the runtime dials back
	// to for daemon-side capabilities. Injected only together with
	// AgentToken — the spec's Daemon Callback contract is
	// both-or-neither, and a URL without a token (or vice versa) is
	// useless to the runtime's clients, which require the pair.
	DaemonURL string

	// AgentToken is the JWT minted by the daemon at CreateAgent. The
	// runtime presents it as `Authorization: Bearer …` on every
	// outbound RPC to DaemonURL. Issued and revoked by the daemon
	// (see internal/auth on the openotters side). Injected only
	// together with DaemonURL; when the pair is absent the runtime
	// degrades gracefully (daemon-backed capability tools simply
	// don't exist).
	AgentToken string
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
//   - PATH                 — strings.Join(BinDirs, ":")
//   - HOME                 — <AgentRoot>/home
//   - XDG_CONFIG_HOME      — <AgentRoot>/home/.config
//   - XDG_CACHE_HOME       — <AgentRoot>/home/.cache
//   - XDG_DATA_HOME        — <AgentRoot>/home/.local/share
//   - TMPDIR               — <AgentRoot>/tmp
//   - LANG                 — C.UTF-8
//   - OTTERS_AGENT_ROOT    — <AgentRoot>
//   - OTTERSD_URL          — <DaemonURL>      (only with AgentToken)
//   - OTTERS_AGENT_TOKEN   — <AgentToken>     (only with DaemonURL)
func BuildLockedEnv(opts EnvOptions) []string {
	home := filepath.Join(opts.AgentRoot, "home")

	env := []string{
		"PATH=" + strings.Join(opts.BinDirs, ":"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"TMPDIR=" + filepath.Join(opts.AgentRoot, "tmp"),
		"LANG=C.UTF-8",
		"OTTERS_AGENT_ROOT=" + opts.AgentRoot,
	}

	// The daemon-callback pair is both-or-neither: a URL without a
	// token (or vice versa) would make the runtime's daemon clients
	// dial and fail Unauthenticated instead of degrading cleanly.
	if opts.DaemonURL != "" && opts.AgentToken != "" {
		env = append(env,
			"OTTERSD_URL="+opts.DaemonURL,
			"OTTERS_AGENT_TOKEN="+opts.AgentToken,
		)
	}

	return env
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
	"PATH":               {},
	"HOME":               {},
	"XDG_CONFIG_HOME":    {},
	"XDG_CACHE_HOME":     {},
	"XDG_DATA_HOME":      {},
	"TMPDIR":             {},
	"LANG":               {},
	"OTTERS_AGENT_ROOT":  {},
	"OTTERSD_URL":        {},
	"OTTERS_AGENT_TOKEN": {},
}

// AppendConfigEnv exports the Agentfile's CONFIG entries onto the
// spawn env as RUNTIME_<UPPER_SNAKE_CASE> variables. The same values
// already land in agent.yaml's `configs:` block (the runtime's
// primary read path), so this is the secondary, env-flavoured copy
// that tooling and subprocess wrappers can read without re-parsing
// the YAML.
//
// Example: CONFIG max-tokens=2048 → RUNTIME_MAX_TOKENS=2048.
//
// Callers apply this before AppendUserEnv, so a user-declared ENV that
// resolves to the same RUNTIME_* name overrides the CONFIG value under
// os/exec last-write-wins semantics.
func AppendConfigEnv(base []string, configs map[string]string) []string {
	if len(configs) == 0 {
		return base
	}

	keys := make([]string, 0, len(configs))
	for k := range configs {
		if k != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	env := slices.Clip(base)
	for _, k := range keys {
		env = append(env, configKeyToEnv(k)+"="+configs[k])
	}

	return env
}

// AppendUserEnv appends user-declared envs onto a base built by
// BuildLockedEnv. Later duplicates win (os/exec last-write-wins). Reserved
// keys and provider-credential suffixes are filtered defensively — spec.Validate
// rejects them at build time, so this is a safety net for a malformed agent.yaml
// — and returned in skipped so the caller can log them.
func AppendUserEnv(base []string, userEnvs []*spec.Env) ([]string, []string) {
	if len(userEnvs) == 0 {
		return base, nil
	}

	env := slices.Clip(base)

	var skipped []string

	for _, e := range userEnvs {
		switch {
		case e == nil || e.Key == "":
			continue
		case isReservedEnvKey(e.Key):
			skipped = append(skipped, e.Key)
		default:
			env = append(env, e.Key+"="+e.Value)
		}
	}

	return env, skipped
}

// isReservedEnvKey reports whether key is one the locked-down env owns or a
// provider-credential name that must not leak through a user ENV.
func isReservedEnvKey(key string) bool {
	if _, reserved := reservedRuntimeEnvKeys[key]; reserved {
		return true
	}

	return strings.HasSuffix(key, "_API_KEY") || strings.HasSuffix(key, "_API_BASE")
}
