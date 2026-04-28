// Package telemetry provides structured logging, OTel tracing, and health
// endpoint helpers for the jira-poller service.
package telemetry

import (
	"log/slog"
	"os"
)

// NewLogger returns a *slog.Logger configured for the given format.
// format="json" → JSONHandler; any other value (including "text" or "") →
// TextHandler. Level is INFO by default; DEBUG when LOG_LEVEL=debug.
func NewLogger(format string) *slog.Logger {
	var lvl slog.Level
	if os.Getenv("LOG_LEVEL") == "debug" {
		lvl = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{Level: lvl}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}
