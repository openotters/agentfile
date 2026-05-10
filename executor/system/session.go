package system

import (
	"context"
	"errors"

	"github.com/openotters/agentfile/executor"
)

// ListSessionMessages fetches the recent messages for sessionID from the
// running runtime's memory store via its gRPC API. Satisfies
// executor.SessionReader. Requires WithAddr + a running runtime subprocess,
// same preconditions as Prompt.
func (a *Agent) ListSessionMessages(
	ctx context.Context, sessionID string, limit int,
) ([]executor.SessionMessage, error) {
	if a.addr == "" {
		return nil, errors.New("agent has no runtime address; configure WithAddr before Run")
	}

	conn, err := a.dial(ctx)
	if err != nil {
		return nil, err
	}

	return executor.FetchSessionMessages(ctx, conn, sessionID, limit)
}

// ListSessions enumerates every session known to the runtime. Satisfies
// executor.SessionLister.
func (a *Agent) ListSessions(ctx context.Context) ([]executor.SessionInfo, error) {
	if a.addr == "" {
		return nil, errors.New("agent has no runtime address; configure WithAddr before Run")
	}

	conn, err := a.dial(ctx)
	if err != nil {
		return nil, err
	}

	return executor.FetchSessions(ctx, conn)
}

// DeleteSession drops sessionID from the runtime's session store.
// Satisfies executor.SessionDeleter.
func (a *Agent) DeleteSession(ctx context.Context, sessionID string) error {
	if a.addr == "" {
		return errors.New("agent has no runtime address; configure WithAddr before Run")
	}

	conn, err := a.dial(ctx)
	if err != nil {
		return err
	}

	return executor.DeleteSessionRPC(ctx, conn, sessionID)
}
