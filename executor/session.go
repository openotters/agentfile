package executor

import (
	"context"
	"time"
)

// SessionMessage is one stored turn from an agent's memory store.
// Role is conventionally "user" or "assistant".
//
// Content shape:
//   - user: prompt text verbatim.
//   - assistant: JSON-encoded array of "parts" (text chunks +
//     tool blocks). BranchesJSON, when non-empty, is a JSON array
//     of alternative parts arrays from regeneration; ActiveBranch
//     indexes which alternative this row's Content represents
//     within the [Content] ++ [BranchesJSON] union.
type SessionMessage struct {
	Role         string
	Content      string
	BranchesJSON string
	ActiveBranch int
	CreatedAt    time.Time
}

// SessionReader retrieves historical messages for a session from the
// running agent's memory store. Optional companion to Prompter /
// StreamPrompter: backends that don't expose session history simply
// don't implement it, and callers should type-assert and handle the
// negative case cleanly.
type SessionReader interface {
	ListSessionMessages(ctx context.Context, sessionID string, limit int) ([]SessionMessage, error)
}

// SessionInfo is one entry in an agent's session log — enough to
// render a chat-history list (id + activity timestamp + message
// count) without fetching the full transcript.
type SessionInfo struct {
	ID           string
	MessageCount int
	LastActive   time.Time
}

// SessionLister enumerates every session the running agent's memory
// store knows about. Same opt-in pattern as SessionReader; backends
// that can't surface this just don't implement it.
type SessionLister interface {
	ListSessions(ctx context.Context) ([]SessionInfo, error)
}

// SessionDeleter removes a single session from the running agent's
// memory store. Same opt-in pattern as SessionReader / SessionLister.
type SessionDeleter interface {
	DeleteSession(ctx context.Context, sessionID string) error
}
