package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/openotters/agentfile/executor"
	agentv1 "github.com/openotters/agentfile/executor/api/v1"
)

// dialReadyTimeout caps how long dial waits for the runtime's gRPC
// server inside the container to move to Ready after start. Same
// budget the system executor uses; the container has to bring up its
// runtime, load model config, validate credentials, and only then
// binds the listener — leaves enough headroom for slow first runs.
const dialReadyTimeout = 20 * time.Second

// Addr returns the host loopback address the runtime's gRPC server
// is published on. Empty until create() has reserved a port (which
// happens at Run time) but otherwise stable for the agent's life.
func (a *Agent) Addr() string {
	if a.deps.hostGRPCPort == "" {
		return ""
	}
	return "127.0.0.1:" + a.deps.hostGRPCPort
}

// Prompt opens a ChatStream and writes the final assistant response
// into w, discarding intermediate tool/step/delta events. Mirrors the
// system executor's Prompter implementation; the only difference is
// that addr points at the host loopback port mapping back into the
// container.
func (a *Agent) Prompt(ctx context.Context, req executor.PromptRequest, w io.Writer) error {
	return a.PromptStream(ctx, req, func(ev executor.PromptEvent) {
		if ev.Type == "message.create" {
			_, _ = io.WriteString(w, ev.Content)
		}
	})
}

// PromptStream opens a ChatStream against the runtime's gRPC server
// and invokes cb synchronously for every event received.
func (a *Agent) PromptStream(ctx context.Context, req executor.PromptRequest, cb func(executor.PromptEvent)) error {
	addr := a.Addr()
	if addr == "" {
		return errors.New("docker agent has no runtime address (Run not called?)")
	}

	conn, err := a.dial(ctx, addr)
	if err != nil {
		return err
	}

	client := agentv1.NewAgentRuntimeClient(conn)

	stream, err := client.ChatStream(ctx, &agentv1.ChatStreamRequest{
		SessionId: req.SessionID,
		Prompt:    req.Prompt,
	})
	if err != nil {
		return fmt.Errorf("opening chat stream: %w", err)
	}

	for {
		ev, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			return nil
		}

		if recvErr != nil {
			return recvErr
		}

		cb(executor.PromptEvent{
			Type:    ev.GetType(),
			Step:    ev.GetStep(),
			Tool:    ev.GetTool(),
			Content: ev.GetContent(),
		})
	}
}

// ListSessionMessages fetches the persisted message log for sessionID
// from the runtime's gRPC server. Satisfies executor.SessionReader.
func (a *Agent) ListSessionMessages(
	ctx context.Context, sessionID string, limit int,
) ([]executor.SessionMessage, error) {
	addr := a.Addr()
	if addr == "" {
		return nil, errors.New("docker agent has no runtime address (Run not called?)")
	}

	conn, err := a.dial(ctx, addr)
	if err != nil {
		return nil, err
	}

	return executor.FetchSessionMessages(ctx, conn, sessionID, limit)
}

// ListSessions enumerates every session in the runtime's memory store.
// Satisfies executor.SessionLister.
func (a *Agent) ListSessions(ctx context.Context) ([]executor.SessionInfo, error) {
	addr := a.Addr()
	if addr == "" {
		return nil, errors.New("docker agent has no runtime address (Run not called?)")
	}

	conn, err := a.dial(ctx, addr)
	if err != nil {
		return nil, err
	}

	return executor.FetchSessions(ctx, conn)
}

// DeleteSession drops sessionID from the runtime's session store.
// Satisfies executor.SessionDeleter.
func (a *Agent) DeleteSession(ctx context.Context, sessionID string) error {
	addr := a.Addr()
	if addr == "" {
		return errors.New("docker agent has no runtime address (Run not called?)")
	}

	conn, err := a.dial(ctx, addr)
	if err != nil {
		return err
	}

	return executor.DeleteSessionRPC(ctx, conn, sessionID)
}

// PromptObject runs a stateless structured-output query against the
// runtime's gRPC server. The runtime handles JSON-schema parsing
// and object marshalling; we just relay.
func (a *Agent) PromptObject(ctx context.Context, req executor.ObjectPromptRequest) ([]byte, error) {
	addr := a.Addr()
	if addr == "" {
		return nil, errors.New("docker agent has no runtime address (Run not called?)")
	}

	conn, err := a.dial(ctx, addr)
	if err != nil {
		return nil, err
	}

	client := agentv1.NewAgentRuntimeClient(conn)

	resp, err := client.PromptObject(ctx, &agentv1.PromptObjectRequest{
		Prompt:     req.Prompt,
		SchemaJson: req.Schema,
		SchemaName: req.SchemaName,
		SchemaDesc: req.SchemaDescription,
	})
	if err != nil {
		return nil, fmt.Errorf("runtime PromptObject: %w", err)
	}

	return resp.GetObjectJson(), nil
}

// dial returns the cached gRPC connection to the runtime, opening
// one on first call. Closed by closeClient on Stop/Remove.
func (a *Agent) dial(ctx context.Context, addr string) (*grpc.ClientConn, error) {
	a.clientMu.Lock()
	defer a.clientMu.Unlock()

	if a.clientConn != nil {
		return a.clientConn, nil
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", addr, err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, dialReadyTimeout)
	defer cancel()

	conn.Connect()

	for {
		s := conn.GetState()
		if s == connectivity.Ready {
			a.clientConn = conn
			return conn, nil
		}

		if !conn.WaitForStateChange(waitCtx, s) {
			_ = conn.Close()

			return nil, fmt.Errorf("runtime at %s not ready within %s: %w",
				addr, dialReadyTimeout, waitCtx.Err())
		}
	}
}
