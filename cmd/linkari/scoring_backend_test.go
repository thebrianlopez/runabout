package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type scoringLogCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *scoringLogCapture) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (c *scoringLogCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.records = append(c.records, r.Clone())
	c.mu.Unlock()
	return nil
}
func (c *scoringLogCapture) WithAttrs(_ []slog.Attr) slog.Handler { return c }
func (c *scoringLogCapture) WithGroup(_ string) slog.Handler      { return c }

func installScoringLogCapture(t *testing.T) *scoringLogCapture {
	t.Helper()
	cap := &scoringLogCapture{}
	orig := slog.Default()
	slog.SetDefault(slog.New(cap))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return cap
}

func (c *scoringLogCapture) findRecord(msg string) (slog.Record, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.records {
		if r.Message == msg {
			return r, true
		}
	}
	return slog.Record{}, false
}

func attrString(r slog.Record, key string) (string, bool) {
	var val string
	found := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			val = a.Value.String()
			found = true
			return false
		}
		return true
	})
	return val, found
}

func TestClaudeCLIScoringBackendName(t *testing.T) {
	if got := (ClaudeCLIScoringBackend{}).Name(); got != "claude_cli" {
		t.Fatalf("Name() = %q, want claude_cli", got)
	}
}

func TestPiScoringBackendName(t *testing.T) {
	if got := (PiScoringBackend{}).Name(); got != "pi" {
		t.Fatalf("Name() = %q, want pi", got)
	}
}

func TestScoringTelemetryComplete(t *testing.T) {
	cap := installScoringLogCapture(t)
	orig := activeScoringBackend
	t.Cleanup(func() { activeScoringBackend = orig })
	activeScoringBackend = fakeScoringBackend{complete: "ok"}

	got, err := execHaiku(context.Background(), "sp", "content")
	if err != nil || got != "ok" {
		t.Fatalf("execHaiku() = %q, %v", got, err)
	}
	assertScoringLog(t, cap, "Complete", "fake", "")
}

func TestScoringTelemetryCompleteJSON(t *testing.T) {
	cap := installScoringLogCapture(t)
	orig := activeScoringBackend
	t.Cleanup(func() { activeScoringBackend = orig })
	activeScoringBackend = fakeScoringBackend{json: []byte(`{"ok":true}`)}

	got, err := execHaikuJSON(context.Background(), "sp", "content", "schema")
	if err != nil || string(got) != `{"ok":true}` {
		t.Fatalf("execHaikuJSON() = %s, %v", string(got), err)
	}
	assertScoringLog(t, cap, "CompleteJSON", "fake", "")
}

func TestScoringTelemetryCompleteVision(t *testing.T) {
	cap := installScoringLogCapture(t)
	orig := activeScoringBackend
	t.Cleanup(func() { activeScoringBackend = orig })
	activeScoringBackend = fakeScoringBackend{vision: []byte(`{"vision":true}`)}

	got, err := execHaikuVision(context.Background(), "sp", "text", "img.png", "schema")
	if err != nil || string(got) != `{"vision":true}` {
		t.Fatalf("execHaikuVision() = %s, %v", string(got), err)
	}
	assertScoringLog(t, cap, "CompleteVision", "fake", "")
}

func TestScoringTelemetryDurationAndError(t *testing.T) {
	cap := installScoringLogCapture(t)
	orig := activeScoringBackend
	t.Cleanup(func() { activeScoringBackend = orig })
	activeScoringBackend = fakeScoringBackend{completeErr: errors.New("boom"), sleep: 10 * time.Millisecond}

	_, err := execHaiku(context.Background(), "sp", "content")
	if err == nil {
		t.Fatal("expected error")
	}
	r, ok := cap.findRecord("scoring_call_complete")
	if !ok {
		t.Fatal("missing scoring_call_complete log")
	}
	if v, ok := attrString(r, "duration_ms"); !ok || v == "" || v == "-1" {
		t.Fatalf("duration_ms invalid: %q, ok=%v", v, ok)
	}
	if v, ok := attrString(r, "error"); !ok || v != "boom" {
		t.Fatalf("error attr = %q, want boom", v)
	}
}

func TestScoringTelemetryNoReturnValueChange(t *testing.T) {
	orig := activeScoringBackend
	t.Cleanup(func() { activeScoringBackend = orig })
	activeScoringBackend = fakeScoringBackend{complete: "same"}

	got, err := execHaiku(context.Background(), "sp", "content")
	if err != nil || got != "same" {
		t.Fatalf("execHaiku() = %q, %v", got, err)
	}
}

type fakeScoringBackend struct {
	complete     string
	completeErr  error
	json         []byte
	jsonErr      error
	vision       []byte
	visionErr    error
	sleep        time.Duration
}

func (f fakeScoringBackend) Name() string { return "fake" }
func (f fakeScoringBackend) Complete(_ context.Context, _, _ string) (string, error) {
	if f.sleep > 0 { time.Sleep(f.sleep) }
	return f.complete, f.completeErr
}
func (f fakeScoringBackend) CompleteJSON(_ context.Context, _, _, _ string) ([]byte, error) {
	if f.sleep > 0 { time.Sleep(f.sleep) }
	return f.json, f.jsonErr
}
func (f fakeScoringBackend) CompleteVision(_ context.Context, _, _, _, _ string) ([]byte, error) {
	if f.sleep > 0 { time.Sleep(f.sleep) }
	return f.vision, f.visionErr
}

func assertScoringLog(t *testing.T, cap *scoringLogCapture, method, backend, errWant string) {
	t.Helper()
	msg := "scoring_call_complete"
	if method == "CompleteJSON" {
		msg = "scoring_call_complete_json"
	} else if method == "CompleteVision" {
		msg = "scoring_call_complete_vision"
	}
	r, ok := cap.findRecord(msg)
	if !ok {
		t.Fatalf("missing log %q", msg)
	}
	if v, ok := attrString(r, "event_type"); !ok || v != "scoring_call" {
		t.Fatalf("event_type = %q, want scoring_call", v)
	}
	if v, ok := attrString(r, "backend"); !ok || v != backend {
		t.Fatalf("backend = %q, want %q", v, backend)
	}
	if v, ok := attrString(r, "method"); !ok || v != method {
		t.Fatalf("method = %q, want %q", v, method)
	}
	if v, ok := attrString(r, "error"); !ok || v != errWant {
		t.Fatalf("error = %q, want %q", v, errWant)
	}
	if v, ok := attrString(r, "duration_ms"); !ok || v == "" {
		t.Fatal("missing duration_ms")
	}
}

var _ io.Reader
