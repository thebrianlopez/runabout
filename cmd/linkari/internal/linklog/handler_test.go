package linklog

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"testing"
)

func TestParseFormat(t *testing.T) {
	cases := []struct {
		in   string
		want Format
		err  bool
	}{
		{"", FormatText, false},
		{"text", FormatText, false},
		{"TEXT", FormatText, false},
		{"json", FormatJSON, false},
		{"JSON", FormatJSON, false},
		{"yaml", FormatText, true},
	}
	for _, tc := range cases {
		got, err := ParseFormat(tc.in)
		if (err != nil) != tc.err {
			t.Errorf("ParseFormat(%q) err=%v want err=%v", tc.in, err, tc.err)
		}
		if !tc.err && got != tc.want {
			t.Errorf("ParseFormat(%q) = %v want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
		err  bool
	}{
		{"", slog.LevelInfo, false},
		{"info", slog.LevelInfo, false},
		{"DEBUG", slog.LevelDebug, false},
		{"warn", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"err", slog.LevelError, false},
		{"trace", slog.LevelInfo, true},
	}
	for _, tc := range cases {
		got, err := ParseLevel(tc.in)
		if (err != nil) != tc.err {
			t.Errorf("ParseLevel(%q) err=%v want err=%v", tc.in, err, tc.err)
		}
		if !tc.err && got != tc.want {
			t.Errorf("ParseLevel(%q) = %v want %v", tc.in, got, tc.want)
		}
	}
}

func newTestHandler(w *bytes.Buffer, format Format, level slog.Level) *Handler {
	return New(w, Options{
		Level:     level,
		Format:    format,
		SessionID: "sess-fixed",
		Command:   "linkari",
		User:      "testuser",
		Cwd:       "/tmp/test",
	})
}

// TestJSONEnvelopeShape asserts the envelope contains all the fields the
// automation-metrics consumers expect.
func TestJSONEnvelopeShape(t *testing.T) {
	var buf bytes.Buffer
	h := newTestHandler(&buf, FormatJSON, slog.LevelDebug)
	logger := slog.New(h)
	logger.Info("hello world", "key", "val")

	var env map[string]any
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, buf.String())
	}

	required := []string{
		"schema_version", "timestamp", "layer", "event_type",
		"command", "session_id", "user", "cwd",
		"duration_ms", "exit_code", "agent", "epic", "milestone", "metadata",
	}
	for _, f := range required {
		if _, ok := env[f]; !ok {
			t.Errorf("envelope missing field %q", f)
		}
	}
	if env["schema_version"] != "2" {
		t.Errorf("schema_version=%v want 2", env["schema_version"])
	}
	if env["layer"] != "linkari" {
		t.Errorf("layer=%v want linkari", env["layer"])
	}
	if env["command"] != "linkari" {
		t.Errorf("command=%v", env["command"])
	}
	if env["session_id"] != "sess-fixed" {
		t.Errorf("session_id=%v", env["session_id"])
	}
	if env["user"] != "testuser" {
		t.Errorf("user=%v", env["user"])
	}
	if env["cwd"] != "/tmp/test" {
		t.Errorf("cwd=%v", env["cwd"])
	}
	// Null envelope fields for a plain log record.
	for _, f := range []string{"duration_ms", "exit_code", "agent", "epic", "milestone"} {
		if env[f] != nil {
			t.Errorf("field %q = %v want null", f, env[f])
		}
	}
	// event_type defaults to "linkari_log" when not promoted.
	if env["event_type"] != "linkari_log" {
		t.Errorf("event_type=%v want linkari_log", env["event_type"])
	}
	// Timestamp format: YYYYMMDDTHHMMSSZ
	ts, _ := env["timestamp"].(string)
	if !regexp.MustCompile(`^\d{8}T\d{6}Z$`).MatchString(ts) {
		t.Errorf("timestamp=%q does not match automation-metrics format", ts)
	}
	md, ok := env["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata is not object: %v", env["metadata"])
	}
	if md["msg"] != "hello world" {
		t.Errorf("metadata.msg=%v", md["msg"])
	}
	if md["level"] != "info" {
		t.Errorf("metadata.level=%v want info", md["level"])
	}
	if md["key"] != "val" {
		t.Errorf("metadata.key=%v", md["key"])
	}
}

// TestEventTypePromotion verifies that event_type, duration_ms, and exit_code
// attrs are promoted to envelope-level fields and removed from metadata.
func TestEventTypePromotion(t *testing.T) {
	var buf bytes.Buffer
	h := newTestHandler(&buf, FormatJSON, slog.LevelDebug)
	logger := slog.New(h)
	logger.Info(
		"req",
		"event_type", "http_request",
		"duration_ms", int64(42),
		"exit_code", int64(0),
		"path", "/share",
	)

	var env map[string]any
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env["event_type"] != "http_request" {
		t.Errorf("event_type=%v want http_request", env["event_type"])
	}
	// JSON unmarshals numbers as float64 by default.
	if env["duration_ms"].(float64) != 42 {
		t.Errorf("duration_ms=%v want 42", env["duration_ms"])
	}
	if env["exit_code"].(float64) != 0 {
		t.Errorf("exit_code=%v want 0", env["exit_code"])
	}
	md := env["metadata"].(map[string]any)
	for _, k := range []string{"event_type", "duration_ms", "exit_code"} {
		if _, exists := md[k]; exists {
			t.Errorf("promoted field %q should not also appear in metadata", k)
		}
	}
	if md["path"] != "/share" {
		t.Errorf("metadata.path=%v", md["path"])
	}
}

// TestTraceIDFromContext verifies trace_id flows from ctx into metadata.
func TestTraceIDFromContext(t *testing.T) {
	var buf bytes.Buffer
	h := newTestHandler(&buf, FormatJSON, slog.LevelDebug)
	logger := slog.New(h)
	ctx := WithTraceID(context.Background(), "trace-abc-123")
	logger.InfoContext(ctx, "hi")

	var env map[string]any
	_ = json.Unmarshal(buf.Bytes(), &env)
	md := env["metadata"].(map[string]any)
	if md["trace_id"] != "trace-abc-123" {
		t.Errorf("trace_id=%v want trace-abc-123", md["trace_id"])
	}
}

func TestTraceIDAbsentWhenNoCtxValue(t *testing.T) {
	var buf bytes.Buffer
	h := newTestHandler(&buf, FormatJSON, slog.LevelDebug)
	logger := slog.New(h)
	logger.Info("plain")
	var env map[string]any
	_ = json.Unmarshal(buf.Bytes(), &env)
	md := env["metadata"].(map[string]any)
	if _, ok := md["trace_id"]; ok {
		t.Errorf("trace_id should be absent when ctx has none")
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	h := newTestHandler(&buf, FormatJSON, slog.LevelWarn)
	logger := slog.New(h)
	logger.Debug("nope-debug")
	logger.Info("nope-info")
	logger.Warn("yes-warn")
	logger.Error("yes-error")
	lines := strings.Count(strings.TrimSpace(buf.String()), "\n") + 1
	if lines != 2 {
		t.Errorf("want 2 lines, got %d: %s", lines, buf.String())
	}
	if strings.Contains(buf.String(), "nope") {
		t.Errorf("filtered output leaked debug/info: %s", buf.String())
	}
}

func TestLevelVarRuntimeChange(t *testing.T) {
	var buf bytes.Buffer
	lvl := new(slog.LevelVar)
	lvl.Set(slog.LevelWarn)
	h := New(&buf, Options{
		Level:     lvl,
		Format:    FormatJSON,
		SessionID: "s",
		Command:   "linkari",
		User:      "t",
		Cwd:       "/tmp",
	})
	logger := slog.New(h)
	logger.Info("hidden")
	if buf.Len() != 0 {
		t.Errorf("info should be filtered, got: %s", buf.String())
	}
	lvl.Set(slog.LevelDebug)
	logger.Info("visible")
	if !strings.Contains(buf.String(), "visible") {
		t.Errorf("info should be emitted after level change, got: %s", buf.String())
	}
}

func TestTextFormat(t *testing.T) {
	var buf bytes.Buffer
	h := newTestHandler(&buf, FormatText, slog.LevelDebug)
	logger := slog.New(h)
	logger.Info("hello", "key", "val")
	s := buf.String()
	if !strings.Contains(s, "hello") {
		t.Errorf("text output missing msg: %s", s)
	}
	if !strings.Contains(s, "key=val") {
		t.Errorf("text output missing key=val: %s", s)
	}
	// Not JSON
	if strings.HasPrefix(strings.TrimSpace(s), "{") {
		t.Errorf("text format should not emit JSON: %s", s)
	}
}

func TestTextFormatWithTraceID(t *testing.T) {
	var buf bytes.Buffer
	h := newTestHandler(&buf, FormatText, slog.LevelDebug)
	logger := slog.New(h)
	ctx := WithTraceID(context.Background(), "trace-text-1")
	logger.InfoContext(ctx, "hello")
	if !strings.Contains(buf.String(), "trace_id=trace-text-1") {
		t.Errorf("text output missing trace_id: %s", buf.String())
	}
}

func TestWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	h := newTestHandler(&buf, FormatJSON, slog.LevelDebug)
	logger := slog.New(h).With("component", "server", "port", 8080)
	logger.Info("boot")
	var env map[string]any
	_ = json.Unmarshal(buf.Bytes(), &env)
	md := env["metadata"].(map[string]any)
	if md["component"] != "server" {
		t.Errorf("component=%v", md["component"])
	}
	if md["port"].(float64) != 8080 {
		t.Errorf("port=%v", md["port"])
	}
}

func TestAutoSessionID(t *testing.T) {
	var buf bytes.Buffer
	h := New(&buf, Options{Format: FormatJSON, Level: slog.LevelDebug, Command: "linkari", User: "t", Cwd: "/tmp"})
	if h.opts.SessionID == "" {
		t.Errorf("SessionID should be auto-generated")
	}
	// Should be a UUID-ish length.
	if len(h.opts.SessionID) < 16 {
		t.Errorf("SessionID=%q too short", h.opts.SessionID)
	}
}

func TestJSONLOneEventPerLine(t *testing.T) {
	var buf bytes.Buffer
	h := newTestHandler(&buf, FormatJSON, slog.LevelDebug)
	logger := slog.New(h)
	logger.Info("a")
	logger.Info("b")
	logger.Info("c")
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d: %s", len(lines), buf.String())
	}
	for i, line := range lines {
		var env map[string]any
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			t.Errorf("line %d not valid JSON: %v", i, err)
		}
	}
}
