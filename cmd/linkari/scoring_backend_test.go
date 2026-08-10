package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
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

	got, err := backendComplete(context.Background(), fakeScoringBackend{complete: "ok"}, "sp", "content")
	if err != nil || got != "ok" {
		t.Fatalf("backendComplete() = %q, %v", got, err)
	}
	assertScoringLog(t, cap, "Complete", "fake", "")
}

func TestScoringTelemetryCompleteJSON(t *testing.T) {
	cap := installScoringLogCapture(t)

	got, err := backendCompleteJSON(context.Background(), fakeScoringBackend{json: []byte(`{"ok":true}`)}, "sp", "content", "schema")
	if err != nil || string(got) != `{"ok":true}` {
		t.Fatalf("backendCompleteJSON() = %s, %v", string(got), err)
	}
	assertScoringLog(t, cap, "CompleteJSON", "fake", "")
}

func TestScoringTelemetryCompleteVision(t *testing.T) {
	cap := installScoringLogCapture(t)

	got, err := backendCompleteVision(context.Background(), fakeScoringBackend{vision: []byte(`{"vision":true}`)}, "sp", "text", "img.png", "schema")
	if err != nil || string(got) != `{"vision":true}` {
		t.Fatalf("backendCompleteVision() = %s, %v", string(got), err)
	}
	assertScoringLog(t, cap, "CompleteVision", "fake", "")
}

func TestScoringTelemetryDurationAndError(t *testing.T) {
	cap := installScoringLogCapture(t)

	_, err := backendComplete(context.Background(), fakeScoringBackend{completeErr: errors.New("boom"), sleep: 10 * time.Millisecond}, "sp", "content")
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
	got, err := backendComplete(context.Background(), fakeScoringBackend{complete: "same"}, "sp", "content")
	if err != nil || got != "same" {
		t.Fatalf("backendComplete() = %q, %v", got, err)
	}
}

type fakeScoringBackend struct {
	complete    string
	completeErr error
	json        []byte
	jsonErr     error
	vision      []byte
	visionErr   error
	sleep       time.Duration
}

func (f fakeScoringBackend) Name() string { return "fake" }
func (f fakeScoringBackend) Complete(_ context.Context, _, _ string) (string, error) {
	if f.sleep > 0 {
		time.Sleep(f.sleep)
	}
	return f.complete, f.completeErr
}

func (f fakeScoringBackend) CompleteJSON(_ context.Context, _, _, _ string) ([]byte, error) {
	if f.sleep > 0 {
		time.Sleep(f.sleep)
	}
	return f.json, f.jsonErr
}

func (f fakeScoringBackend) CompleteVision(_ context.Context, _, _, _, _ string) ([]byte, error) {
	if f.sleep > 0 {
		time.Sleep(f.sleep)
	}
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

// RG-3: backend attribution must be single-sourced from activeScoringBackend.
//
// POMO scoring-backend-attribution-split-brain. Evaluator labels were
// compile-time constants ("claude-haiku-json") that kept reporting Claude after
// EPIC-246 routed execution through ScoringBackend, so score_prefilter_summary
// and Scorecard.Backend disagreed with the scoring_call telemetry. The fake
// backend is deliberately named "fake" — any hardcoded claude* constant fails
// this test by construction.
func TestRG3_BackendAttributionMatchesActiveBackend(t *testing.T) {
	prev := activeScoringBackend
	t.Cleanup(func() { activeScoringBackend = prev })
	activeScoringBackend = fakeScoringBackend{}

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"markdown evaluator", HaikuMarkdownEvaluator{}.Name(), "fake:md"},
		{"json evaluator", HaikuJSONEvaluator{}.Name(), "fake:json"},
		{"vision evaluator", HaikuVisionEvaluator{}.Name(), "fake:vision"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: Name() = %q, want %q (attribution must resolve from activeScoringBackend)", tc.name, tc.got, tc.want)
		}
		if strings.Contains(tc.got, "claude") {
			t.Errorf("%s: Name() = %q leaks a hardcoded claude label while backend is %q", tc.name, tc.got, activeScoringBackend.Name())
		}
	}
}

// RG-3b: Scorecard.Backend must agree with eval.Name() for the same call.
// This is the field that reaches the persisted archive and the CLI analytics
// sinks (cmd_triage.go, cmd_score.go).
func TestRG3b_ScorecardBackendMatchesEvaluatorName(t *testing.T) {
	backend := &funcScoringBackend{
		name: "fake",
		completeJSON: func(_ context.Context, _, _, _ string) ([]byte, error) {
			return []byte(`{"type":"result","result":"{\"score\":42,\"verdict\":\"ok\",\"rubric_scores\":{\"relevance\":50,\"depth\":40}}","is_error":false}`), nil
		},
	}

	eval := HaikuJSONEvaluator{Backend: backend}
	sc, err := eval.Evaluate(context.Background(), "content", "prompt")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if sc.Backend != eval.Name() {
		t.Errorf("Scorecard.Backend = %q, eval.Name() = %q; the two attribution sinks must agree", sc.Backend, eval.Name())
	}
	if sc.Backend != "fake:json" {
		t.Errorf("Scorecard.Backend = %q, want %q", sc.Backend, "fake:json")
	}
}
