package linklog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// Format selects the on-wire output shape for a Handler.
type Format int

const (
	// FormatText emits human-friendly key=value lines via stdlib
	// slog.TextHandler. Default for interactive sessions.
	FormatText Format = iota
	// FormatJSON emits automation-metrics envelope JSONL
	// (schema_version "2"). Use for ingestion / eval harness.
	FormatJSON
)

// ParseFormat parses a string flag value into a Format.
// Accepts "text" or "json" (case-insensitive). Empty string → FormatText.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "text":
		return FormatText, nil
	case "json":
		return FormatJSON, nil
	default:
		return FormatText, fmt.Errorf("unknown log format %q (want text|json)", s)
	}
}

// ParseLevel parses a string flag value into a slog.Level.
// Accepts "debug"|"info"|"warn"|"error" (case-insensitive). Empty → LevelInfo.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error", "err":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level %q (want debug|info|warn|error)", s)
	}
}

// Options configures a Handler.
type Options struct {
	// Level is the minimum level that will be emitted. Leveled via slog.LevelVar
	// for runtime adjustment if the caller wires it.
	Level slog.Leveler

	// Format selects text vs JSON envelope output.
	Format Format

	// SessionID stamps every event. Auto-generated (UUID) if empty.
	SessionID string

	// Command populates the envelope "command" field. Defaults to "linkari".
	Command string

	// User populates the envelope "user" field. Defaults to $USER.
	User string

	// Cwd populates the envelope "cwd" field. Defaults to os.Getwd().
	Cwd string
}

// Handler is a slog.Handler that emits events in the automation-metrics
// envelope shape as JSONL, or delegates to slog.TextHandler for text mode.
type Handler struct {
	opts   Options
	w      io.Writer
	mu     *sync.Mutex
	text   slog.Handler // non-nil iff Format == FormatText
	attrs  []slog.Attr  // pre-accumulated via WithAttrs
	groups []string     // reserved: slog groups are flattened in JSON mode
}

// New builds a Handler writing to w with the given options.
// Missing Options fields are auto-populated from environment/defaults.
func New(w io.Writer, opts Options) *Handler {
	if opts.Command == "" {
		opts.Command = "linkari"
	}
	if opts.SessionID == "" {
		opts.SessionID = uuid.NewString()
	}
	if opts.User == "" {
		opts.User = os.Getenv("USER")
	}
	if opts.Cwd == "" {
		if cwd, err := os.Getwd(); err == nil {
			opts.Cwd = cwd
		}
	}
	if opts.Level == nil {
		opts.Level = slog.LevelInfo
	}
	h := &Handler{
		opts: opts,
		w:    w,
		mu:   &sync.Mutex{},
	}
	if opts.Format == FormatText {
		h.text = slog.NewTextHandler(w, &slog.HandlerOptions{Level: opts.Level})
	}
	return h
}

// Enabled implements slog.Handler.
func (h *Handler) Enabled(_ context.Context, lvl slog.Level) bool {
	return lvl >= h.opts.Level.Level()
}

// Handle implements slog.Handler.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	if h.text != nil {
		return h.handleText(ctx, r)
	}
	return h.handleJSON(ctx, r)
}

func (h *Handler) handleText(ctx context.Context, r slog.Record) error {
	th := h.text
	if len(h.attrs) > 0 {
		th = th.WithAttrs(h.attrs)
	}
	for _, g := range h.groups {
		th = th.WithGroup(g)
	}
	// Inject trace_id from ctx as an inline attr so it appears in the text line.
	if tid := TraceIDFromContext(ctx); tid != "" {
		r.AddAttrs(slog.String("trace_id", tid))
	}
	return th.Handle(ctx, r)
}

func (h *Handler) handleJSON(ctx context.Context, r slog.Record) error {
	meta := make(map[string]any, 4+r.NumAttrs()+len(h.attrs))
	meta["level"] = strings.ToLower(r.Level.String())
	meta["msg"] = r.Message

	// Pre-accumulated attrs (from WithAttrs). These are considered context,
	// so they go into metadata (and may be promoted below if they carry
	// envelope-level keys like trace_id).
	for _, a := range h.attrs {
		if a.Key == "" {
			continue
		}
		meta[a.Key] = a.Value.Resolve().Any()
	}

	// Record attrs — with promotion of envelope-level keys.
	var (
		eventType  string
		durationMs *int64
		exitCode   *int
	)
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "event_type":
			eventType = a.Value.String()
		case "duration_ms":
			v := a.Value.Int64()
			durationMs = &v
		case "exit_code":
			v := int(a.Value.Int64())
			exitCode = &v
		default:
			meta[a.Key] = a.Value.Resolve().Any()
		}
		return true
	})

	// trace_id from ctx wins over any stale attr in metadata.
	if tid := TraceIDFromContext(ctx); tid != "" {
		meta["trace_id"] = tid
	}

	if eventType == "" {
		eventType = "linkari_log"
	}

	// Timestamp format matches automation-metrics: compact ISO 8601 UTC.
	ts := r.Time.UTC().Format("20060102T150405Z")

	env := map[string]any{
		"schema_version": "2",
		"timestamp":      ts,
		"layer":          "linkari",
		"event_type":     eventType,
		"command":        h.opts.Command,
		"session_id":     h.opts.SessionID,
		"user":           h.opts.User,
		"cwd":            h.opts.Cwd,
		"duration_ms":    durationMs, // nil → JSON null
		"exit_code":      exitCode,   // nil → JSON null
		"agent":          nil,
		"epic":           nil,
		"milestone":      nil,
		"metadata":       meta,
	}

	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("linklog marshal: %w", err)
	}
	data = append(data, '\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err = h.w.Write(data)
	return err
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	nh := *h
	nh.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	if h.text != nil {
		nh.text = h.text.WithAttrs(attrs)
	}
	return &nh
}

// WithGroup implements slog.Handler.
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := *h
	nh.groups = append(append([]string(nil), h.groups...), name)
	if h.text != nil {
		nh.text = h.text.WithGroup(name)
	}
	return &nh
}
