package executor

import (
	"context"
	"errors"
	"time"
)

// Note is one entry in the per-agent notes store. The Content field
// is only populated by GetNote and Save responses; ListNotes returns
// notes with empty Content to keep payloads small. Preview is
// denormalised server-side (first non-empty line, ≤ 80 runes).
type Note struct {
	Key       string
	Content   string
	Preview   string
	InContext bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SaveResult bundles the saved note with a flag indicating whether
// it replaced an existing key. The daemon UI surfaces "overwrote"
// vs "created" to the operator so a typo doesn't silently clobber
// a prior fact.
type SaveResult struct {
	Note      Note
	Overwrote bool
}

// ErrNoteNotFound is returned by NotesAPI.GetNote, DeleteNote, and
// SetNoteInContext when the key isn't present. Callers can use
// errors.Is to distinguish a missing key from a transport failure
// or a quota violation.
var ErrNoteNotFound = errors.New("note not found")

// NotesAPI is the capability-interface for the per-agent notes
// store, mirroring the runtime's pkg/notes Store API. Executors
// (docker, system) implement it by proxying to the runtime's gRPC
// NotesService.
//
// The daemon casts agentpkg.Agent to NotesAPI when handling the
// operator-facing notes RPCs (list, get, save, delete, pin/unpin).
// Agents that don't expose this interface — e.g. legacy executors
// or stubs — surface "agent does not support notes" at the daemon
// boundary rather than at the gRPC layer.
type NotesAPI interface {
	ListNotes(ctx context.Context, onlyInContext bool) ([]Note, error)
	GetNote(ctx context.Context, key string) (Note, error)
	SaveNote(ctx context.Context, key, content string, maxBytes, maxCount int32) (SaveResult, error)
	DeleteNote(ctx context.Context, key string) (existed bool, err error)
	SetNoteInContext(ctx context.Context, key string, inContext bool) (Note, error)
}
