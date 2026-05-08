package executor

import (
	"context"

	"github.com/google/uuid"

	"github.com/openotters/agentfile/spec"
)

// Provider manages agent lifecycle on a specific backend (system, docker, etc.).
//
// Each Provider also exposes the storage backend agents are stored
// in via Registry(). The system Provider's Registry wraps an
// embedded oras-go store; the Docker Provider's Registry wraps the
// Docker daemon's image store. Daemon image-management RPCs
// (List / Build / Pull / Push / Remove / Inspect) route through
// Registry() instead of accessing storage directly so the operator's
// choice of executor determines where artifacts live.
type Provider interface {
	// Create materializes and prepares an agent with the given ID, returning it ready to Run.
	Create(ctx context.Context, id uuid.UUID, ref spec.Reference, opts ...spec.Override) (Agent, error)

	// Load recovers previously created agents from the backend.
	Load(ctx context.Context) ([]Agent, error)

	// Destroy removes all agents and associated artifacts.
	Destroy(ctx context.Context) error

	// Registry returns the OCI artifact storage backend this
	// Provider is bound to. Never nil; backends that don't yet
	// support every Registry method return ErrNotImplemented.
	Registry() Registry
}
