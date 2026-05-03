//go:build linux

//nolint:testpackage // direct internal access
package system

import (
	"errors"
	"reflect"
	"slices"
	"testing"
)

func TestBuildBwrapFlags_BaseShape(t *testing.T) {
	t.Parallel()

	flags := buildBwrapFlags(sandboxParams{
		AgentRoot:  "/agents/abc",
		RuntimeBin: "/agents/abc/usr/local/bin/runtime",
	})

	for _, pair := range [][]string{
		{"--unshare-pid"},
		{"--unshare-ipc"},
		{"--die-with-parent"},
		{"--ro-bind", "/", "/"},
		{"--proc", "/proc"},
		{"--dev", "/dev"},
		{"--bind", "/agents/abc", "/agents/abc"},
		{"--ro-bind", "/agents/abc/usr/local/bin/runtime", "/agents/abc/usr/local/bin/runtime"},
		{"--tmpfs", "/tmp"},
		{"--chdir", "/agents/abc/workspace"},
	} {
		if !sliceContainsSeq(flags, pair) {
			t.Errorf("missing flag sequence %v in: %v", pair, flags)
		}
	}
}

func TestBuildBwrapFlags_NetworkOff(t *testing.T) {
	t.Parallel()

	flags := buildBwrapFlags(sandboxParams{
		AgentRoot:      "/agents/abc",
		RuntimeBin:     "/agents/abc/usr/local/bin/runtime",
		NetworkAllowed: false,
	})

	if !slices.Contains(flags, "--unshare-net") {
		t.Errorf("network-off agent missing --unshare-net: %v", flags)
	}
}

func TestBuildBwrapFlags_NetworkOn(t *testing.T) {
	t.Parallel()

	flags := buildBwrapFlags(sandboxParams{
		AgentRoot:      "/agents/abc",
		RuntimeBin:     "/agents/abc/usr/local/bin/runtime",
		NetworkAllowed: true,
	})

	if slices.Contains(flags, "--unshare-net") {
		t.Errorf("network-on agent must not unshare net: %v", flags)
	}
}

func TestBuildBwrapFlags_MountsRoVsRw(t *testing.T) {
	t.Parallel()

	flags := buildBwrapFlags(sandboxParams{
		AgentRoot:  "/agents/abc",
		RuntimeBin: "/agents/abc/usr/local/bin/runtime",
		Mounts: []sandboxMount{
			{Host: "/Users/me/proj", RW: true},
			{Host: "/Users/me/readonly", RW: false},
		},
	})

	if !sliceContainsSeq(flags, []string{"--bind", "/Users/me/proj", "/Users/me/proj"}) {
		t.Errorf("rw mount missing --bind seq: %v", flags)
	}
	if !sliceContainsSeq(flags, []string{"--ro-bind", "/Users/me/readonly", "/Users/me/readonly"}) {
		t.Errorf("ro mount missing --ro-bind seq: %v", flags)
	}
}

func TestLinuxWrapper_PrependsBwrap(t *testing.T) {
	t.Parallel()

	saved := bwrapLookPath
	bwrapLookPath = func() (string, error) { return "/usr/bin/bwrap", nil }
	t.Cleanup(func() { bwrapLookPath = saved })

	w := newWrapper(sandboxParams{
		AgentRoot:  "/agents/abc",
		RuntimeBin: "/agents/abc/usr/local/bin/runtime",
	})

	if w.Name() != "bwrap" {
		t.Fatalf("Name() = %q, want bwrap", w.Name())
	}

	out := w.Wrap([]string{"/agents/abc/usr/local/bin/runtime", "serve"})
	if out[0] != "/usr/bin/bwrap" {
		t.Fatalf("wrap argv[0] = %q, want /usr/bin/bwrap (full path on $PATH)", out[0])
	}

	// "--" must separate flags from the command argv.
	dashIdx := slices.Index(out, "--")
	if dashIdx == -1 {
		t.Fatalf("wrap argv missing '--' separator: %v", out)
	}
	tail := out[dashIdx+1:]
	if !reflect.DeepEqual(tail, []string{"/agents/abc/usr/local/bin/runtime", "serve"}) {
		t.Fatalf("wrap argv tail = %v, want original argv", tail)
	}
}

func TestLinuxWrapper_FallsBackToNoopWhenBwrapMissing(t *testing.T) {
	t.Parallel()

	saved := bwrapLookPath
	bwrapLookPath = func() (string, error) { return "", errors.New("bwrap not on PATH") }
	t.Cleanup(func() { bwrapLookPath = saved })

	w := newWrapper(sandboxParams{
		AgentRoot:  "/agents/abc",
		RuntimeBin: "/agents/abc/usr/local/bin/runtime",
	})

	if w.Name() != "none" {
		t.Errorf("Name() = %q, want none (bwrap absent)", w.Name())
	}

	in := []string{"/agents/abc/usr/local/bin/runtime", "serve"}
	out := w.Wrap(in)
	if !reflect.DeepEqual(out, in) {
		t.Errorf("noop fallback rewrote argv: got %v, want %v", out, in)
	}
}

func TestWrapperKind_Linux(t *testing.T) {
	t.Parallel()

	saved := bwrapLookPath
	t.Cleanup(func() { bwrapLookPath = saved })

	bwrapLookPath = func() (string, error) { return "/usr/bin/bwrap", nil }
	if got := wrapperKind(); got != "bwrap" {
		t.Errorf("wrapperKind() with bwrap present = %q, want bwrap", got)
	}

	bwrapLookPath = func() (string, error) { return "", errors.New("missing") }
	if got := wrapperKind(); got != "none" {
		t.Errorf("wrapperKind() without bwrap = %q, want none", got)
	}
}

// sliceContainsSeq reports whether s contains the contiguous run of
// values in seq. Used to assert that bwrap two-token flag pairs land
// in the right order (e.g. `--bind <src> <dst>`).
func sliceContainsSeq[T comparable](s, seq []T) bool {
	if len(seq) == 0 {
		return true
	}
	for i := 0; i+len(seq) <= len(s); i++ {
		match := true
		for j, v := range seq {
			if s[i+j] != v {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
