package executor

import (
	"context"
	"time"

	"google.golang.org/grpc"

	agentv1 "github.com/openotters/agentfile/executor/api/v1"
)

// FetchSessionMessages calls the runtime's ListSessionMessages RPC on
// conn and adapts the wire response into executor.SessionMessage.
// The system and docker executors differ only in how they obtain the
// gRPC connection (loopback port vs. forwarded container port); the
// RPC shape itself is identical, so the marshalling lives here.
func FetchSessionMessages(
	ctx context.Context, conn *grpc.ClientConn, sessionID string, limit int,
) ([]SessionMessage, error) {
	client := agentv1.NewAgentRuntimeClient(conn)

	resp, err := client.ListSessionMessages(ctx, &agentv1.ListSessionMessagesRequest{
		SessionId: sessionID,
		Limit:     int32(limit), //nolint:gosec // caller-bounded
	})
	if err != nil {
		return nil, err
	}

	raw := resp.GetMessages()
	out := make([]SessionMessage, len(raw))

	for i, m := range raw {
		out[i] = SessionMessage{
			Role:         m.GetRole(),
			Content:      m.GetContent(),
			BranchesJSON: m.GetBranchesJson(),
			ActiveBranch: int(m.GetActiveBranch()),
			CreatedAt:    time.Unix(m.GetCreatedAt(), 0),
		}
	}

	return out, nil
}

// FetchSessions calls the runtime's ListSessions RPC on conn and
// adapts the wire response into executor.SessionInfo.
func FetchSessions(ctx context.Context, conn *grpc.ClientConn) ([]SessionInfo, error) {
	client := agentv1.NewAgentRuntimeClient(conn)

	resp, err := client.ListSessions(ctx, &agentv1.ListSessionsRequest{})
	if err != nil {
		return nil, err
	}

	raw := resp.GetSessions()
	out := make([]SessionInfo, len(raw))

	for i, s := range raw {
		out[i] = SessionInfo{
			ID:           s.GetId(),
			MessageCount: int(s.GetMessageCount()),
			LastActive:   time.Unix(s.GetLastActive(), 0),
		}
	}

	return out, nil
}

// DeleteSessionRPC drops sessionID from the runtime's session store.
// Idempotent on the runtime side; success is reported even when the
// session was already gone.
func DeleteSessionRPC(ctx context.Context, conn *grpc.ClientConn, sessionID string) error {
	client := agentv1.NewAgentRuntimeClient(conn)

	if _, err := client.DeleteSession(ctx, &agentv1.DeleteSessionRequest{
		SessionId: sessionID,
	}); err != nil {
		return err
	}

	return nil
}
