package utils

import (
	"fmt"

	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

type RemoteRepositoryOption func(*remote.Repository)

func WithPlainHTTP(repo *remote.Repository) {
	repo.PlainHTTP = true
}

func NewRemoteRepository(ref string, opts ...RemoteRepositoryOption) (*remote.Repository, error) {
	repo, err := remote.NewRepository(ref)
	if err != nil {
		return nil, fmt.Errorf("parsing reference %s: %w", ref, err)
	}

	for _, opt := range opts {
		opt(repo)
	}

	credStore, err := credentials.NewStoreFromDocker(credentials.StoreOptions{})
	if err != nil {
		return nil, fmt.Errorf("loading credentials: %w", err)
	}

	repo.Client = &auth.Client{Credential: credentials.Credential(credStore)}

	return repo, nil
}
