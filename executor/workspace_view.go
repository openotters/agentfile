package executor

// WorkspaceView captures the agent-visible filesystem layout — the
// paths the runtime feeds into WORKSPACE.md so the LLM has an
// accurate mental model of where things live in *its* world.
//
// The two backends produce different views:
//
//   - System executor: host-rooted, no sandbox. Root is the real
//     on-disk path of the agent's materialised tree
//     (`/Users/<me>/.otters/agents/<id>`). Tool calls must use
//     these absolute paths verbatim.
//
//   - Docker executor: container-rooted at `/workspace`. The host
//     directory is bind-mounted under `/workspace`; runtime +
//     BINs are image-mounted under `/opt/`. The agent never sees
//     the host path of its tree.
//
// A zero-value WorkspaceView is treated as "use the system
// defaults": Root = the workspace's billy root, BinDirs = a single
// `<Root>/usr/bin`, RuntimeBin = `<Root>/usr/local/bin/runtime`,
// Isolated = false.
type WorkspaceView struct {
	// Root is what the agent considers the root of its tree —
	// the directory under which `etc/`, `home/`, `tmp/`, `var/`
	// live (and `workspace/` when WorkspaceDir is empty).
	// OTTERS_AGENT_ROOT in the spawned env points here.
	Root string

	// WorkspaceDir is the agent-visible path to the scratch dir.
	// When empty, falls back to `<Root>/workspace`. Docker
	// overrides this to `/workspace` so the user-facing CWD is
	// not `/agent/workspace` but a clean top-level path; the
	// host directory is bind-mounted there in addition to the
	// FHS root.
	WorkspaceDir string

	// BinDirs are the absolute paths the runtime resolves BIN
	// tools against, in the agent's view. System: a single
	// `<Root>/usr/bin`. Docker: one entry per BIN, e.g.
	// `/opt/bins/ping`, `/opt/bins/jq`.
	BinDirs []string

	// RuntimeBin is the absolute path of the runtime binary in
	// the agent's view. System: `<Root>/usr/local/bin/runtime`.
	// Docker: `/opt/runtime/runtime`.
	RuntimeBin string

	// Isolated reports whether a real sandbox boundary separates
	// the host from the agent. Docker: true. System: false.
	// Affects WORKSPACE.md phrasing — isolated agents are told
	// "you're in a container; everything below is the in-container
	// path"; non-isolated ones are told "no chroot, real host
	// paths in tool calls".
	Isolated bool
}
