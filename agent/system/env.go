package system

import (
	"encoding/json"
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
//   - OTTERS_AGENT_ROOT + OTTERS_MOUNTS expose the agent's view of
//     its own filesystem to the runtime so it can render
//     Workspace.md from real paths.
//
// Notably NOT inherited: the host's PATH, HOME, SSH_AUTH_SOCK,
// AWS_*, GITHUB_TOKEN, anything else. Each host-process secret
// stays on the host.
//
// Callers append per-provider credential entries (<PROVIDER>_API_KEY
// / <PROVIDER>_API_BASE) on top of this base; those values live on
// the resolved AgentRuntime, not on the host environment.
func BuildLockedEnv(agentRoot string, mounts []Mount) []string {
	home := filepath.Join(agentRoot, "home")

	env := []string{
		"PATH=" + filepath.Join(agentRoot, "usr", "bin"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"TMPDIR=" + filepath.Join(agentRoot, "tmp"),
		"LANG=C.UTF-8",
		"OTTERS_AGENT_ROOT=" + agentRoot,
		"OTTERS_MOUNTS=" + mountsJSON(mounts),
	}

	return env
}

// mountsJSON serialises mounts to a JSON array suitable for the
// runtime to read + render into Workspace.md. Empty slice
// serialises to "[]" (never null) so the runtime's parser can
// always trust the shape.
//
// Mount's public-API field names are upper-case (Host, Target,
// Description); the wire shape is lower-case so it reads well in
// the env var dump and matches the Workspace.md template's
// preferred casing. The ad-hoc wireMount struct keeps the marshal
// independent of any future Mount field additions that don't
// belong on the wire.
func mountsJSON(mounts []Mount) string {
	if len(mounts) == 0 {
		return "[]"
	}

	type wireMount struct {
		Host        string `json:"host"`
		Target      string `json:"target,omitempty"`
		Description string `json:"description,omitempty"`
	}

	out := make([]wireMount, 0, len(mounts))
	for _, m := range mounts {
		out = append(out, wireMount(m)) //nolint:gosimple // intentional copy with distinct json tags
	}

	b, err := json.Marshal(out)
	if err != nil {
		// Should never happen for a fixed shape; degrade
		// gracefully so a JSON edge-case can't fail Provider
		// startup.
		return "[]"
	}

	return string(b)
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
