// Package linklog provides a slog.Handler that emits JSONL events in the
// automation-metrics envelope shape (schema_version "2") for linkari.
//
// It is the foundation for EPIC-051 (structured logging migration). The
// handler supports two output formats:
//
//   - FormatText — stdlib slog.TextHandler, for local DevEx (tail-friendly)
//   - FormatJSON — automation-metrics envelope JSONL, for ingestion
//
// A trace_id stored in context.Context (via WithTraceID) is automatically
// surfaced on every log line, enabling request correlation.
package linklog

import "context"

type traceIDKey struct{}

// WithTraceID returns a copy of ctx with the given trace_id attached.
// The value is surfaced on every slog record emitted via a linklog.Handler.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	if traceID == "" {
		return ctx
	}
	return context.WithValue(ctx, traceIDKey{}, traceID)
}

// TraceIDFromContext returns the trace_id stored in ctx, or the empty
// string if none is present.
func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(traceIDKey{}).(string)
	return v
}
