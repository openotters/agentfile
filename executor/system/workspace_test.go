//nolint:testpackage // direct internal access
package system

import (
	"strings"
	"testing"

	"github.com/openotters/agentfile/executor"
)

func TestWorkspaceContextMarkdown_IncludesRoot(t *testing.T) {
	t.Parallel()

	md := string(workspaceContextMarkdown("/var/agents/abc"))

	for _, needle := range []string{
		"# Your workspace",
		"Your workspace on the host: `/var/agents/abc`",
		"`/var/agents/abc/etc/context/`",
		"`/var/agents/abc/etc/data/`",
		"`/var/agents/abc/usr/bin/`",
		"`/var/agents/abc/usr/local/bin/runtime`",
		"`/var/agents/abc/workspace/`",
		"`/var/agents/abc/tmp/`",
		"`/var/agents/abc/var/lib/`",
		"MOUNTS.md",
	} {
		if !strings.Contains(md, needle) {
			t.Fatalf("missing %q in output:\n%s", needle, md)
		}
	}

	// And the chroot-style short forms must NOT appear by themselves —
	// emitting them would let the agent re-learn the broken paths.
	for _, forbidden := range []string{
		"`/etc/context/`",
		"`/etc/data/`",
		"`/usr/bin/`",
		"`/usr/local/bin/runtime`",
		"`/workspace/`",
	} {
		if strings.Contains(md, forbidden) {
			t.Fatalf("chroot-style short form %q must not appear in output:\n%s", forbidden, md)
		}
	}
}

func TestWorkspaceContextMarkdownView_DockerContainer(t *testing.T) {
	t.Parallel()

	md := string(workspaceContextMarkdownView(executor.WorkspaceView{
		Root:         "/agent",
		WorkspaceDir: "/workspace",
		RuntimeBin:   "/opt/runtime/runtime",
		BinDirs:      []string{"/opt/bins/ping", "/opt/bins/jq"},
		Isolated:     true,
	}))

	// FHS root surfaces under /agent; the user-facing scratch is
	// the top-level /workspace bind, not /agent/workspace.
	for _, needle := range []string{
		"# Your workspace",
		"You run inside an isolated container",
		"Your workspace inside the container: `/agent`",
		"`/agent/etc/context/`",
		"`/agent/etc/data/`",
		"`/opt/bins/ping/`",
		"`/opt/bins/jq/`",
		"`/opt/runtime/runtime`",
		"`/workspace/`",
		"`/agent/home/`",
		"`/agent/tmp/`",
		"The container isolates you from the host filesystem",
	} {
		if !strings.Contains(md, needle) {
			t.Fatalf("missing %q in output:\n%s", needle, md)
		}
	}

	// The doubled-up /workspace/workspace/ path that confused the
	// model before must not reappear.
	if strings.Contains(md, "/workspace/workspace") {
		t.Fatalf("doubled /workspace/workspace path leaked into container view:\n%s", md)
	}

	// The host-rooted phrasing must NOT leak into the container view.
	for _, forbidden := range []string{
		"Your workspace on the host",
		"There is no chroot",
		"/usr/bin/", // system-default BIN dir
		"/usr/local/bin/runtime",
	} {
		if strings.Contains(md, forbidden) {
			t.Fatalf("host-rooted phrase %q must not appear in container view:\n%s", forbidden, md)
		}
	}
}

func TestWorkspaceContextMarkdown_EmptyRootOmitsHostLine(t *testing.T) {
	t.Parallel()

	md := string(workspaceContextMarkdown(""))

	// Header + layout must still be present.
	if !strings.HasPrefix(md, "# Your workspace\n") {
		t.Fatalf("missing header:\n%s", md)
	}

	// But the "on the host" hint must be suppressed when we don't know
	// the real path (tests don't always have a billy.Root()).
	if strings.Contains(md, "on the host") {
		t.Fatalf("empty root should omit host hint:\n%s", md)
	}
}

func TestMountsContextMarkdown_RendersEachMount(t *testing.T) {
	t.Parallel()

	md := string(mountsContextMarkdown([]executor.Mount{
		{Host: "/home/me/proj", Target: "/workspace/proj", Description: "project checkout"},
		{Host: "/tmp", Target: "/scratch"}, // no description
	}))

	for _, needle := range []string{
		"# Host-mounted paths",
		"`/workspace/proj`",
		"— project checkout",
		"`/scratch`",
	} {
		if !strings.Contains(md, needle) {
			t.Fatalf("missing %q in output:\n%s", needle, md)
		}
	}

	// No-description mount should render just the target, without a
	// trailing em-dash. Check the specific line.
	for _, line := range strings.Split(md, "\n") {
		if strings.Contains(line, "`/scratch`") && strings.Contains(line, "—") {
			t.Fatalf("mount without Description must not include em-dash: %q", line)
		}
	}
}

func TestMountsContextMarkdown_Empty(t *testing.T) {
	t.Parallel()

	md := string(mountsContextMarkdown(nil))

	// Header + explainer still render; no list items follow.
	if !strings.HasPrefix(md, "# Host-mounted paths\n") {
		t.Fatalf("missing header:\n%s", md)
	}

	if strings.Contains(md, "\n- ") {
		t.Fatalf("empty mount list must produce no bullets:\n%s", md)
	}
}
