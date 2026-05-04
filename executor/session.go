package executor

import (
	"context"
	"time"
)

// SessionMessage is one stored turn from an agent's memory store.
// Role is conventionally "user" or "assistant".
type SessionMessage struct {
	Role      string
	Content   string
	CreatedAt time.Time
}

// SessionReader retrieves historical messages for a session from the
// running agent's memory store. Optional companion to Prompter /
// StreamPrompter: backends that don't expose session history simply
// don't implement it, and callers should type-assert and handle the
// negative case cleanly.
type SessionReader interface {
	ListSessionMessages(ctx context.Context, sessionID string, limit int) ([]SessionMessage, error)
}
