package system

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

// See docker/notes.go for the doc-heavy sibling; the system
// executor mirrors the same proxy shape with its own dial helper.

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

func (a *Agent) notesClient(ctx context.Context) (agentv1.AgentRuntimeClient, error) {
	if a.addr == "" {
		return nil, errors.New("system agent has no runtime address (Run not called?)")
	}
	conn, err := a.dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("dial runtime: %w", err)
	}
	return agentv1.NewAgentRuntimeClient(conn), nil
}

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
