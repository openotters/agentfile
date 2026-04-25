// Package oci wraps oras-go for agentfile use: authenticated remote
// repositories, manifest/index resolution, blob fetching, and a Puller
// abstraction that extracts a bin layer from an image.
package oci

import (
	"fmt"
	"net"
	"net/http"

	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"

	"github.com/openotters/agentfile/spec"
)

// RemoteRepositoryOption mutates a remote repository at construction time.
type RemoteRepositoryOption func(*remote.Repository)

// WithPlainHTTP configures the repository to use HTTP instead of HTTPS.
// Suitable for local/dev registries only.
func WithPlainHTTP(repo *remote.Repository) {
	repo.PlainHTTP = true
}

// NewRemoteRepository constructs an oras remote.Repository bound to
// ref, wired up with Docker credential resolution and a retrying
// transport that tolerates transient 5xx + transport errors. The
// retry is what lets the first-ever push of a brand-new GHCR package
// succeed: GHCR provisions backend storage on the first manifest PUT
// and the request can race the provisioning step.
func NewRemoteRepository(ref spec.Reference, opts ...RemoteRepositoryOption) (*remote.Repository, error) {
	repo, err := remote.NewRepository(ref.String())
	if err != nil {
		return nil, fmt.Errorf("parsing reference %s: %w", ref, err)
	}

	credStore, err := credentials.NewStoreFromDocker(credentials.StoreOptions{})
	if err != nil {
		return nil, fmt.Errorf("loading credentials: %w", err)
	}

	repo.Client = &auth.Client{
		Client:     &http.Client{Transport: withRetry(http.DefaultTransport)},
		Credential: credentials.Credential(credStore),
	}

	// Mirror Docker's default-resolver behavior: loopback hosts speak
	// HTTP. Caller-supplied opts run after, so explicit choices win.
	if isLoopbackHost(repo.Reference.Registry) {
		repo.PlainHTTP = true
	}

	for _, opt := range opts {
		opt(repo)
	}

	return repo, nil
}

// isLoopbackHost reports whether the given "host" or "host:port"
// string targets the local loopback. Matches Docker/containerd: the
// literal "localhost", anything in 127.0.0.0/8, and ::1.
func isLoopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}

	if host == "localhost" {
		return true
	}

	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}

	return false
}
