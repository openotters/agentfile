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

func TestNewRemoteRepository_InvalidRef(t *testing.T) {
	t.Parallel()

	// Names with only whitespace can't be a valid distribution name.
	if _, err := oci.NewRemoteRepository(spec.Reference{Name: "  ", Tag: "latest"}); err == nil {
		t.Fatal("NewRemoteRepository(whitespace) = nil, want error")
	}
}
