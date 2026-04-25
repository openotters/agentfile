//nolint:testpackage // direct internal access
package system

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/osfs"
	"github.com/google/uuid"
)

// --- applyMounts on memfs -----------------------------------------------

func TestApplyMounts_CreatesSymlinksOnHostFS(t *testing.T) {
	t.Parallel()

	hostFS := memfs.New()

	// Build a chroot-ish filesystem that has a real Root() string —
	// osfs.New("/tmp/agent") works without touching disk as long as
	// we don't actually write through it; but easier: since fs.Root()
	// is the only thing applyMounts reads off fs (it then hands the
	// link path to hostFS), we can use any non-empty-rooted billy.
	chroot := osfs.New("/tmp/mock-agent")

	w := &workspace{
		hostFS: hostFS,
		mounts: []Mount{
			{Host: "/home/me/proj", Target: "/workspace/proj", Description: "project"},
			{Host: "/tmp", Target: "/scratch"},
		},
	}

	if err := w.applyMounts(chroot); err != nil {
		t.Fatalf("applyMounts: %v", err)
	}

	// applyMounts should have created symlinks at chroot-root + target,
	// resolved on hostFS. Verify with hostFS.Lstat (billy doesn't expose
	// Readlink uniformly in v6 — Lstat returning a symlink mode is the
	// portable check).
	assertSymlink(t, hostFS, "/tmp/mock-agent/workspace/proj")
	assertSymlink(t, hostFS, "/tmp/mock-agent/scratch")
}

func TestApplyMounts_IsIdempotent(t *testing.T) {
	t.Parallel()

	hostFS := memfs.New()
	chroot := osfs.New("/tmp/mock-agent-idem")

	w := &workspace{
		hostFS: hostFS,
		mounts: []Mount{{Host: "/src", Target: "/mnt"}},
	}

	if err := w.applyMounts(chroot); err != nil {
		t.Fatalf("first applyMounts: %v", err)
	}

	// Re-running must succeed — Remove-then-Symlink is the pattern,
	// and memfs doesn't error on removing a non-existent path when it
	// already exists.
	if err := w.applyMounts(chroot); err != nil {
		t.Fatalf("second applyMounts: %v", err)
	}

	assertSymlink(t, hostFS, "/tmp/mock-agent-idem/mnt")
}

func TestApplyMounts_EmptyTargetErrors(t *testing.T) {
	t.Parallel()

	w := &workspace{
		hostFS: memfs.New(),
		mounts: []Mount{{Host: "/src", Target: "/"}}, // trims to "" after prefix strip
	}

	err := w.applyMounts(osfs.New("/tmp/any"))
	if err == nil {
		t.Fatal("applyMounts(empty target) = nil, want error")
	}

	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestApplyMounts_NoMountsIsNoOp(t *testing.T) {
	t.Parallel()

	hostFS := memfs.New()

	w := &workspace{hostFS: hostFS, mounts: nil}

	// No mounts → no hostFS calls, no MOUNTS.md write, no error.
	// Pass a nil fs to prove the short-circuit triggers before we
	// dereference anything.
	if err := w.applyMounts(nil); err != nil {
		t.Fatalf("applyMounts(empty) = %v, want nil", err)
	}
}

func TestApplyMounts_RequiresNonEmptyRoot(t *testing.T) {
	t.Parallel()

	w := &workspace{
		hostFS: memfs.New(),
		mounts: []Mount{{Host: "/src", Target: "/mnt"}},
	}

	// memfs.New().Root() returns "/" → non-empty, accepted.
	// A billy filesystem with Root() == "" would be rejected; simulate
	// by asserting the error message references "real root directory".
	err := w.applyMounts(emptyRootFS{})
	if err == nil {
		t.Fatal("applyMounts with empty-rooted fs = nil, want error")
	}

	if !strings.Contains(err.Error(), "real root directory") {
		t.Fatalf("unexpected err: %v", err)
	}
}

// --- Provider.openLogFile on memfs --------------------------------------

func TestOpenLogFile_WithHostFS(t *testing.T) {
	t.Parallel()

	hostFS := memfs.New()

	p := &Provider{
		hostFS: hostFS,
		logDir: "/var/log/otters",
	}

	id := uuid.MustParse("11111111-2222-3333-4444-555555555555")

	w, err := p.openLogFile(id)
	if err != nil {
		t.Fatalf("openLogFile: %v", err)
	}

	if w == nil {
		t.Fatal("openLogFile returned nil writer despite non-empty logDir")
	}

	// Writing lands in memfs — no host disk touched.
	if _, e := w.Write([]byte("hello\n")); e != nil {
		t.Fatalf("write: %v", e)
	}

	expected := "/var/log/otters/" + id.String() + ".log"
	if _, e := hostFS.Stat(expected); e != nil {
		t.Fatalf("expected log file %q to exist: %v", expected, e)
	}
}

func TestOpenLogFile_EmptyDirReturnsNilWriter(t *testing.T) {
	t.Parallel()

	p := &Provider{hostFS: memfs.New(), logDir: ""}

	w, err := p.openLogFile(uuid.New())
	if err != nil {
		t.Fatalf("openLogFile(empty dir) err = %v, want nil", err)
	}

	if w != nil {
		t.Fatalf("openLogFile(empty dir) writer = %v, want nil", w)
	}
}

// --- NewProvider default hostFS ----------------------------------------

func TestNewProvider_DefaultHostFSIsOsfsAtRoot(t *testing.T) {
	t.Parallel()

	p := NewProvider(memfs.New(), nil)

	if p.hostFS == nil {
		t.Fatal("NewProvider left hostFS nil")
	}

	// osfs.New("/") reports Root == "/" — this confirms the default
	// we intend (real host disk).
	if p.hostFS.Root() != "/" {
		t.Fatalf("default hostFS Root = %q, want /", p.hostFS.Root())
	}
}

func TestWithHostFS_Overrides(t *testing.T) {
	t.Parallel()

	injected := memfs.New()

	p := NewProvider(memfs.New(), nil, WithHostFS(injected))

	if p.hostFS != injected {
		t.Fatal("WithHostFS did not override the default")
	}
}

// --- helpers ------------------------------------------------------------

func assertSymlink(t *testing.T, fs billy.Filesystem, path string) {
	t.Helper()

	info, err := fs.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(%q): %v", path, err)
	}

	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected %q to be a symlink, got mode %v", path, info.Mode())
	}
}

// emptyRootFS wraps memfs to return "" from Root() so the
// "real root directory" branch of applyMounts is reachable in tests
// without poking at billy internals.
type emptyRootFS struct{ billy.Filesystem }

func (emptyRootFS) Root() string { return "" }

// Compile-time assertion that emptyRootFS satisfies billy.Filesystem.
var _ billy.Filesystem = emptyRootFS{Filesystem: memfs.New()}

// unused helper that proves filepath is still imported when tests
// run in isolation — prevents "imported and not used" if we trim.
var _ = filepath.Join
