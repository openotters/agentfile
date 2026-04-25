package agent

import (
	"context"

	"github.com/google/uuid"

	"github.com/openotters/agentfile/spec"
)

// Provider manages agent lifecycle on a specific backend (system, docker, etc.).
type Provider interface {
	// Create materializes and prepares an agent with the given ID, returning it ready to Run.
	Create(ctx context.Context, id uuid.UUID, ref spec.Reference, opts ...spec.Override) (Agent, error)

	// Load recovers previously created agents from the backend.
	Load(ctx context.Context) ([]Agent, error)

	// Destroy removes all agents and associated artifacts.
	Destroy(ctx context.Context) error
}
