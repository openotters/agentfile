package executor

import (
	"context"
	"io"
)

// streamSinksKey is the unexported context key for ExecStreamSinks.
// Defined as a private struct type so external packages can't collide.
type streamSinksKey struct{}

// ExecStreamSinks carries optional io.Writer destinations for live
// stdout / stderr forwarding while a BIN is executing. The fields
// are independent: either or both may be nil and the executor must
// tolerate that without panicking. Writers SHOULD be safe for
// concurrent calls from the executor's stdout / stderr demux paths.
//
// The executor still returns the full ExecResult.Stdout /
// ExecResult.Stderr on completion — the sinks are an additional
// progress channel, not a replacement. This way callers that don't
// care about live output (tests, the chat path) keep working
// unchanged.
type ExecStreamSinks struct {
	Stdout io.Writer
	Stderr io.Writer
}

// WithExecStreamSinks attaches stream sinks to ctx so executor
// implementations forward live BIN output as bytes arrive. Mainly
// used by the openotters async-jobs pool to push partial output
// into SQLite while the BIN is still running — UI / CLI observers
// then see growing logs instead of a blank pane until terminal
// status.
//
// Sinks travel through context (rather than a new Exec method
// argument) so the existing Agent.Exec signature stays the same
// and backends that don't implement live-streaming keep compiling.
// Backends that DO implement it call ExecStreamSinksFrom inside
// their Exec method.
func WithExecStreamSinks(ctx context.Context, sinks ExecStreamSinks) context.Context {
	return context.WithValue(ctx, streamSinksKey{}, sinks)
}

// ExecStreamSinksFrom returns the sinks attached via
// WithExecStreamSinks, or a zero value (both Writers nil) when none
// were set. Executors should treat nil Writers as "no live forwarding"
// — they're still expected to fill ExecResult.Stdout / Stderr in the
// returned ExecResult either way.
func ExecStreamSinksFrom(ctx context.Context) ExecStreamSinks {
	s, _ := ctx.Value(streamSinksKey{}).(ExecStreamSinks)

	return s
}
