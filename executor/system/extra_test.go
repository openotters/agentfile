//nolint:testpackage // direct internal access
package system

import (
	"os"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/osfs"
	"github.com/google/uuid"

	"github.com/openotters/agentfile/executor"
)

// TestReapplyMounts_EmptyIsNoop covers the fast-path branch — an agent
// with no mounts short-circuits without touching hostFS.
func TestReapplyMounts_EmptyIsNoop(t *testing.T) {
	t.Parallel()

	a := NewAgent(uuid.New(), memfs.New())

	if err := a.ReapplyMounts(); err != nil {
		t.Fatalf("ReapplyMounts(no mounts) = %v, want nil", err)
	}
}

// TestReapplyMounts_Delegates confirms the method drives the same
// applyMounts path we already unit-tested in hostfs_test.go, wired
// through the public API. hostFS is memfs so no real disk touched.
func TestReapplyMounts_Delegates(t *testing.T) {
	t.Parallel()

	hostFS := memfs.New()
	// fs.Root() must be non-empty for applyMounts — osfs backed by
	// tmpdir does that without touching anything; the actual symlink
	// lands on hostFS (memfs) because applyMounts uses a.ws.hostFS.
	chroot := osfs.New("/tmp/reapply-test")

	a := NewAgent(uuid.New(), chroot,
		WithMounts([]executor.Mount{{Host: "/home/me/code", Target: "/workspace/code"}}),
	)
	a.ws.hostFS = hostFS

	if err := a.ReapplyMounts(); err != nil {
		t.Fatalf("ReapplyMounts: %v", err)
	}

	info, err := hostFS.Lstat("/tmp/reapply-test/workspace/code")
	if err != nil {
		t.Fatalf("expected symlink at target: %v", err)
	}

	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("target is not a symlink: mode=%v", info.Mode())
	}
}
