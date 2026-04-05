package oci

import (
	"context"
	"fmt"

	"github.com/openotters/agentfile/spec"
	afstore "github.com/openotters/agentfile/store"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
)

const defaultTag = "latest"

// AgentFetcher returns a resolve.Fetcher that pulls agent artifacts from OCI registries.
func AgentFetcher(opts ...RemoteRepositoryOption) func(ctx context.Context, ref string) (*spec.Agentfile, error) {
	return func(ctx context.Context, ref string) (*spec.Agentfile, error) {
		repo, err := NewRemoteRepository(ref, opts...)
		if err != nil {
			return nil, err
		}

		tag := repo.Reference.Reference
		if tag == "" {
			tag = defaultTag
		}

		store := memory.New()

		desc, err := oras.Copy(ctx, repo, tag, store, tag, oras.DefaultCopyOptions)
		if err != nil {
			return nil, fmt.Errorf("pulling %s: %w", ref, err)
		}

		if tag != defaultTag {
			if tagErr := store.Tag(ctx, desc, defaultTag); tagErr != nil {
				return nil, tagErr
			}
		}

		return afstore.LoadWithLayers(store, defaultTag)
	}
}
