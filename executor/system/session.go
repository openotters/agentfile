package system

import (
	"context"
	"errors"
	"time"

	"github.com/openotters/agentfile/executor"
	agentv1 "github.com/openotters/agentfile/executor/api/v1"
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

	client := agentv1.NewAgentRuntimeClient(conn)

	resp, err := client.ListSessionMessages(ctx, &agentv1.ListSessionMessagesRequest{
		SessionId: sessionID,
		Limit:     int32(limit), //nolint:gosec // caller-bounded
	})
	if err != nil {
		return nil, err
	}

	raw := resp.GetMessages()
	out := make([]executor.SessionMessage, len(raw))

	for i, m := range raw {
		out[i] = executor.SessionMessage{
			Role:      m.GetRole(),
			Content:   m.GetContent(),
			CreatedAt: time.Unix(m.GetCreatedAt(), 0),
		}
	}

	return out, nil
}
