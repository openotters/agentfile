package docker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/openotters/agentfile/executor"
	agentv1 "github.com/openotters/agentfile/executor/api/v1"
)

// ListNotes proxies to the runtime's gRPC NotesService. When
// onlyInContext is true the runtime filters to in_context = 1.
// Content is intentionally omitted from list responses to keep
// payloads small; clients call GetNote for the body.
func (a *Agent) ListNotes(ctx context.Context, onlyInContext bool) ([]executor.Note, error) {
	client, err := a.notesClient(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := client.ListNotes(ctx, &agentv1.ListNotesRequest{OnlyInContext: onlyInContext})
	if err != nil {
		return nil, fmt.Errorf("runtime ListNotes: %w", err)
	}

	out := make([]executor.Note, 0, len(resp.GetNotes()))
	for _, n := range resp.GetNotes() {
		out = append(out, noteFromProto(n))
	}
	return out, nil
}

func (a *Agent) GetNote(ctx context.Context, key string) (executor.Note, error) {
	client, err := a.notesClient(ctx)
	if err != nil {
		return executor.Note{}, err
	}

	resp, err := client.GetNote(ctx, &agentv1.GetNoteRequest{Key: key})
	if err != nil {
		return executor.Note{}, mapNotFound(err, "GetNote")
	}
	return noteFromProto(resp.GetNote()), nil
}

func (a *Agent) SaveNote(
	ctx context.Context, key, content string, maxBytes, maxCount int32,
) (executor.SaveResult, error) {
	client, err := a.notesClient(ctx)
	if err != nil {
		return executor.SaveResult{}, err
	}

	resp, err := client.SaveNote(ctx, &agentv1.SaveNoteRequest{
		Key: key, Content: content, MaxBytes: maxBytes, MaxCount: maxCount,
	})
	if err != nil {
		return executor.SaveResult{}, fmt.Errorf("runtime SaveNote: %w", err)
	}
	return executor.SaveResult{
		Note:      noteFromProto(resp.GetNote()),
		Overwrote: resp.GetOverwrote(),
	}, nil
}

func (a *Agent) DeleteNote(ctx context.Context, key string) (bool, error) {
	client, err := a.notesClient(ctx)
	if err != nil {
		return false, err
	}

	resp, err := client.DeleteNote(ctx, &agentv1.DeleteNoteRequest{Key: key})
	if err != nil {
		return false, fmt.Errorf("runtime DeleteNote: %w", err)
	}
	return resp.GetDeleted(), nil
}

func (a *Agent) SetNoteInContext(ctx context.Context, key string, inContext bool) (executor.Note, error) {
	client, err := a.notesClient(ctx)
	if err != nil {
		return executor.Note{}, err
	}

	resp, err := client.SetNoteInContext(ctx, &agentv1.SetNoteInContextRequest{
		Key: key, InContext: inContext,
	})
	if err != nil {
		return executor.Note{}, mapNotFound(err, "SetNoteInContext")
	}
	return noteFromProto(resp.GetNote()), nil
}

// notesClient returns the AgentRuntime client wired to the
// container's gRPC port, dialing on first use and reusing the
// connection for subsequent calls. Mirrors the dial/cache shape of
// the chat path.
func (a *Agent) notesClient(ctx context.Context) (agentv1.AgentRuntimeClient, error) {
	addr := a.Addr()
	if addr == "" {
		return nil, errors.New("docker agent has no runtime address (Run not called?)")
	}
	conn, err := a.dial(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("dial runtime: %w", err)
	}
	return agentv1.NewAgentRuntimeClient(conn), nil
}

// mapNotFound rewrites a gRPC NotFound into the package's typed
// sentinel so callers (the daemon) can errors.Is-check against
// executor.ErrNoteNotFound without parsing the gRPC code at every
// call site.
func mapNotFound(err error, op string) error {
	if s, ok := status.FromError(err); ok && s.Code() == codes.NotFound {
		return executor.ErrNoteNotFound
	}
	return fmt.Errorf("runtime %s: %w", op, err)
}

func noteFromProto(n *agentv1.Note) executor.Note {
	if n == nil {
		return executor.Note{}
	}
	return executor.Note{
		Key:       n.GetKey(),
		Content:   n.GetContent(),
		Preview:   n.GetPreview(),
		InContext: n.GetInContext(),
		CreatedAt: unixToTime(n.GetCreatedAt()),
		UpdatedAt: unixToTime(n.GetUpdatedAt()),
	}
}

func unixToTime(secs int64) time.Time {
	if secs == 0 {
		return time.Time{}
	}
	return time.Unix(secs, 0).UTC()
}
