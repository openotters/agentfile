package system

import (
	"path/filepath"
	"strings"
)

// BuildLockedEnv returns the curated environment the runtime
// subprocess (and every tool descendant) should see. Notably:
//
//   - PATH points only at <agent-root>/usr/bin so tool subprocesses
//     find the agent's pinned BIN binaries, never the host's.
//   - HOME and XDG_* live inside the agent tree so tools that touch
//     ~/.cache / ~/.config (wget's HSTS file, curl's netrc, etc.)
//     write inside the agent root rather than polluting the
//     operator's real home.
//   - TMPDIR points inside the agent root.
//   - LANG=C.UTF-8 for predictable locale-dependent output.
//   - OTTERS_AGENT_ROOT lets `sh -c` invocations address the agent
//     tree without hardcoding the path.
//
// Notably NOT inherited: the host's PATH, HOME, SSH_AUTH_SOCK,
// AWS_*, GITHUB_TOKEN, anything else. Each host-process secret
// stays on the host.
//
// Callers append per-provider credential entries (<PROVIDER>_API_KEY
// / <PROVIDER>_API_BASE) on top of this base; those values live on
// the resolved Runtime, not on the host environment.
func BuildLockedEnv(agentRoot string) []string {
	home := filepath.Join(agentRoot, "home")

	return []string{
		"PATH=" + filepath.Join(agentRoot, "usr", "bin"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"TMPDIR=" + filepath.Join(agentRoot, "tmp"),
		"LANG=C.UTF-8",
		"OTTERS_AGENT_ROOT=" + agentRoot,
	}
}

// envIndex returns the index of the first KEY= entry in env, or -1
// if absent. Used by tests / extras to add or override entries
// after BuildLockedEnv runs.
func envIndex(env []string, key string) int {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			return i
		}
	}

	return -1
}
