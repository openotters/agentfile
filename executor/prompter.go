package executor

import (
	"context"
	"io"
)

// PromptRequest carries a chat request addressed at a specific session.
// Empty SessionID lets the runtime pick/create a default session.
type PromptRequest struct {
	SessionID string
	Prompt    string
	// Regenerate signals the runtime to attach the produced parts
	// as a new branch onto the most recent assistant turn for
	// SessionID instead of inserting a fresh row.
	Regenerate bool
}

// PromptEvent is a typed chat event yielded by StreamPrompter. Mirrors the
// runtime's gRPC ChatStreamEvent so callers can render progress (steps,
// tool calls, streaming text) without merging everything into one blob.
//
// Known Type values: "step.start", "step.finish", "text.delta",
// "tool.call", "tool.result", "message.create", "error".
type PromptEvent struct {
	Type    string
	Step    int32
	Tool    string
	Content string
}

// Prompter returns the full assistant response as a single blob, discarding
// intermediate events. Use for simple unary request/response callers.
type Prompter interface {
	Prompt(ctx context.Context, req PromptRequest, w io.Writer) error
}

// StreamPrompter delivers typed events as they arrive from the runtime's
// gRPC server — steps, tool calls, token deltas, and the final
// message.create with the rendered response. cb is invoked synchronously
// from the stream goroutine; slow callbacks backpressure the stream.
type StreamPrompter interface {
	PromptStream(ctx context.Context, req PromptRequest, cb func(PromptEvent)) error
}

// ObjectPromptRequest carries a one-shot structured-output request:
// a user prompt plus a JSON Schema describing the response shape.
// No session id — structured generation is stateless and bypasses the
// agent's tool loop. SchemaName / SchemaDescription surface in
// tool-mode providers as the synthetic tool's name / description;
// they're optional.
type ObjectPromptRequest struct {
	Prompt            string
	Schema            []byte
	SchemaName        string
	SchemaDescription string
}

// ObjectPrompter generates a JSON object that conforms to the
// supplied schema. The returned bytes are valid JSON suitable for
// piping into jq or for json.Unmarshal. Rare providers may produce
// invalid JSON even with repair; callers should treat a returned
// error as "no usable object" rather than checking bytes shape.
type ObjectPrompter interface {
	PromptObject(ctx context.Context, req ObjectPromptRequest) ([]byte, error)
}
