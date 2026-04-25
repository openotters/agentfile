//nolint:testpackage // direct internal access
package system

import (
	"strings"
	"testing"
)

func TestWorkspaceContextMarkdown_IncludesRoot(t *testing.T) {
	t.Parallel()

	md := string(workspaceContextMarkdown("/var/agents/abc"))

	for _, needle := range []string{
		"# Your workspace",
		"Your workspace on the host: `/var/agents/abc`",
		"`/etc/context/`",
		"`/etc/data/`",
		"`/usr/bin/`",
		"`/usr/local/bin/runtime`",
		"`/workspace/`",
		"`/tmp/`",
		"`/var/lib/`",
		"MOUNTS.md",
	} {
		if !strings.Contains(md, needle) {
			t.Fatalf("missing %q in output:\n%s", needle, md)
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

	md := string(mountsContextMarkdown([]Mount{
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
