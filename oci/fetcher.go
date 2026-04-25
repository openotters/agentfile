package oci

import (
	"context"
	"fmt"

	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/spec"
	afstore "github.com/openotters/agentfile/store"
)

// AgentFetcher returns a resolve.Fetcher that pulls agent artifacts from OCI registries.
func AgentFetcher(
	opts ...RemoteRepositoryOption,
) func(ctx context.Context, ref spec.Reference) (*spec.Agentfile, error) {
	return func(ctx context.Context, ref spec.Reference) (*spec.Agentfile, error) {
		repo, err := NewRemoteRepository(ref, opts...)
		if err != nil {
			return nil, err
		}

		tag := repo.Reference.Reference
		if tag == "" {
			tag = spec.DefaultTag
		}

		store := memory.New()

		desc, err := oras.Copy(ctx, repo, tag, store, tag, oras.DefaultCopyOptions)
		if err != nil {
			return nil, fmt.Errorf("pulling %s: %w", ref, err)
		}

		if tag != spec.DefaultTag {
			if tagErr := store.Tag(ctx, desc, spec.DefaultTag); tagErr != nil {
				return nil, tagErr
			}
		}

		return afstore.LoadHydrated(ctx, store, spec.ParseReference(spec.DefaultTag))
	}
}
