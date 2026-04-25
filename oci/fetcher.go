package oci

import (
	"context"
	"fmt"

	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/spec"
	afstore "github.com/openotters/agentfile/store"
)

// AgentFetcher returns a resolve.Fetcher that pulls agent artifacts
// from a remote OCI registry.
func AgentFetcher(
	opts ...RemoteRepositoryOption,
) func(ctx context.Context, ref spec.Reference) (*spec.Agentfile, error) {
	return func(ctx context.Context, ref spec.Reference) (*spec.Agentfile, error) {
		repo, err := NewRemoteRepository(ref, opts...)
		if err != nil {
			return nil, err
		}

		return LoadAgentFromSource(ctx, repo, ref)
	}
}

// LoadAgentFromSource copies the manifest at ref out of src into a
// fresh in-memory oras store and returns the hydrated Agentfile.
//
// Exposed (vs. inlined into AgentFetcher) so callers can test the
// fetch logic against an in-memory source — no httptest registry, no
// daemon — and so non-remote sources (e.g. an embedded local
// registry) can reuse the same load path.
func LoadAgentFromSource(
	ctx context.Context, src oras.ReadOnlyTarget, ref spec.Reference,
) (*spec.Agentfile, error) {
	srcTag := ref.Tag
	if srcTag == "" {
		srcTag = spec.DefaultTag
	}

	store := memory.New()

	// Pull from src using its tag, but tag the in-memory destination
	// with the full ref string so LoadHydrated below can resolve it
	// via ref.String(). Avoids the earlier ParseReference("latest")
	// trap that produced "latest:latest" and 404'd against the store.
	if _, err := oras.Copy(ctx, src, srcTag, store, ref.String(), oras.DefaultCopyOptions); err != nil {
		return nil, fmt.Errorf("pulling %s: %w", ref, err)
	}

	return afstore.LoadHydrated(ctx, store, ref)
}
