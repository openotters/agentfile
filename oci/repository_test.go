package oci_test

import (
	"testing"

	"github.com/openotters/agentfile/oci"
	"github.com/openotters/agentfile/spec"
)

func TestParseTag(t *testing.T) {
	t.Parallel()

	if got := oci.ParseTag(spec.Reference{Name: "meteo", Tag: "v1"}); got != "v1" {
		t.Fatalf("ParseTag = %q, want v1", got)
	}
}

func TestNewRemoteRepository(t *testing.T) {
	t.Parallel()

	// A well-formed public ref constructs cleanly even when the host is
	// unreachable — construction shouldn't issue any network call.
	repo, err := oci.NewRemoteRepository(spec.Reference{Name: "ghcr.io/openotters/agents/meteo", Tag: "v1"})
	if err != nil {
		t.Fatalf("NewRemoteRepository: %v", err)
	}

	if repo.Reference.Reference != "v1" {
		t.Fatalf("repo.Reference.Reference = %q, want v1", repo.Reference.Reference)
	}

	if repo.PlainHTTP {
		t.Fatal("PlainHTTP should default to false")
	}
}

func TestNewRemoteRepository_WithPlainHTTP(t *testing.T) {
	t.Parallel()

	repo, err := oci.NewRemoteRepository(
		spec.Reference{Name: "localhost:5000/meteo", Tag: "latest"},
		oci.WithPlainHTTP,
	)
	if err != nil {
		t.Fatalf("NewRemoteRepository: %v", err)
	}

	if !repo.PlainHTTP {
		t.Fatal("WithPlainHTTP did not set PlainHTTP=true")
	}
}

// TestNewRemoteRepository_LoopbackAutoPlainHTTP pins Docker's default:
// loopback hosts (localhost, 127.0.0.0/8, ::1) flip PlainHTTP=true
// without any caller opt-in. Anything else — including private-network
// ranges (RFC1918) — stays on HTTPS unless the caller explicitly asks
// otherwise. Mirrors what containerd/buildkit/oras-go's "default
// resolver" decide for the same hosts.
func TestNewRemoteRepository_LoopbackAutoPlainHTTP(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		refName string
		want    bool
	}{
		{"loopback ipv4 with port", "127.0.0.1:5527/foo", true},
		{"loopback ipv4 in /8", "127.7.7.7:5527/foo", true},
		{"localhost with port", "localhost:5000/foo", true},
		{"localhost no port", "localhost/foo", true},
		{"loopback ipv6 with port", "[::1]:5000/foo", true},
		{"public host", "ghcr.io/openotters/foo", false},
		{"private LAN ipv4", "192.168.1.5:5000/foo", false},
		{"private LAN class A", "10.0.0.1:5000/foo", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo, err := oci.NewRemoteRepository(spec.Reference{Name: tc.refName, Tag: "latest"})
			if err != nil {
				t.Fatalf("NewRemoteRepository: %v", err)
			}

			if repo.PlainHTTP != tc.want {
				t.Errorf("PlainHTTP for %q = %v, want %v", tc.refName, repo.PlainHTTP, tc.want)
			}
		})
	}
}

func TestNewRemoteRepository_InvalidRef(t *testing.T) {
	t.Parallel()

	// Names with only whitespace can't be a valid distribution name.
	if _, err := oci.NewRemoteRepository(spec.Reference{Name: "  ", Tag: "latest"}); err == nil {
		t.Fatal("NewRemoteRepository(whitespace) = nil, want error")
	}
}
