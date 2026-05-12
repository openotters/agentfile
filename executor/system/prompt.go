package system

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"google.golang.org/grpc"

	"github.com/openotters/agentfile/executor"
	agentv1 "github.com/openotters/agentfile/executor/api/v1"
)

// dialReadyTimeout caps how long dial() waits for the runtime's gRPC
// server to move to the Ready state after the subprocess has been started.
// The runtime process has to load the model config, open SQLite, validate
// credentials, and only then binds its listener, so a bare Dial may race.
const dialReadyTimeout = 20 * time.Second

// Prompt opens a ChatStream and writes the final assistant response into w,
// discarding intermediate tool/step/delta events. Prefer PromptStream when
// you need per-event progress.
func (a *Agent) Prompt(ctx context.Context, req executor.PromptRequest, w io.Writer) error {
	return a.PromptStream(ctx, req, func(ev executor.PromptEvent) {
		if ev.Type == "message.create" {
			_, _ = io.WriteString(w, ev.Content)
		}
	})
}

// PromptStream opens a ChatStream against the runtime's gRPC server and
// invokes cb synchronously for every event received. Returns when the
// stream closes or ctx is cancelled. Requires the agent to have been
// configured with WithAddr and the runtime to be running.
func (a *Agent) PromptStream(ctx context.Context, req executor.PromptRequest, cb func(executor.PromptEvent)) error {
	if a.addr == "" {
		return errors.New("agent has no runtime address; configure WithAddr before Run")
	}

	conn, err := a.dial(ctx)
	if err != nil {
		return err
	}

	client := agentv1.NewAgentRuntimeClient(conn)

	stream, err := client.ChatStream(ctx, &agentv1.ChatStreamRequest{
		SessionId:  req.SessionID,
		Prompt:     req.Prompt,
		Regenerate: req.Regenerate,
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

// PromptObject runs a one-shot structured-output query against the
// runtime's LanguageModel. Stateless: no session memory, no tool
// loop. The runtime's PromptObject RPC handles parsing the JSON
// schema and marshalling the resulting object.
func (a *Agent) PromptObject(ctx context.Context, req executor.ObjectPromptRequest) ([]byte, error) {
	if a.addr == "" {
		return nil, errors.New("agent has no runtime address; configure WithAddr before Run")
	}

	conn, err := a.dial(ctx)
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

// Probe issues a single Ready() RPC against the running runtime.
// Returns nil when the runtime answers Ready=true; any dial failure,
// transport error, or Ready=false response is surfaced as an error.
//
// The daemon supervisor calls Probe in a retry loop after Run sets
// StatusStarting; the first successful Probe transitions the agent
// to StatusReady.
func (a *Agent) Probe(ctx context.Context) error {
	if a.addr == "" {
		return errors.New("agent has no runtime address; configure WithAddr before Run")
	}

	conn, err := a.dial(ctx)
	if err != nil {
		return fmt.Errorf("dial runtime: %w", err)
	}

	client := agentv1.NewAgentRuntimeClient(conn)
	resp, err := client.Ready(ctx, &agentv1.ReadyRequest{})
	if err != nil {
		return fmt.Errorf("runtime Ready RPC: %w", err)
	}

	if !resp.GetReady() {
		return errors.New("runtime reported ready=false")
	}

	return nil
}

// dial returns the cached gRPC connection to the runtime, opening one on
// first call via a.dialer. The connection is closed by closeClient on
// Stop/Remove. Timeout + Ready-wait semantics live inside the Dialer
// (defaultDialer by default; tests inject a bufconn-backed dialer).
func (a *Agent) dial(ctx context.Context) (*grpc.ClientConn, error) {
	a.clientMu.Lock()
	defer a.clientMu.Unlock()

	if a.clientConn != nil {
		return a.clientConn, nil
	}

	conn, err := a.dialer.Dial(ctx, a.addr)
	if err != nil {
		return nil, err
	}

	a.clientConn = conn

	return conn, nil
}

func (a *Agent) closeClient() {
	a.clientMu.Lock()
	conn := a.clientConn
	a.clientConn = nil
	a.clientMu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
}
