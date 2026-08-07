package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/thebrianlopez/runabout/cmd/linkari/internal/linklog"
)

// traceCapture records every (ctx, record) pair passed to slog's Handle method.
// It is installed as the global slog default during trace_id tests.
type traceCapture struct {
	mu      sync.Mutex
	records []capturedLogRecord
}

type capturedLogRecord struct {
	ctx   context.Context
	level slog.Level
	attrs map[string]any
}

func (h *traceCapture) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *traceCapture) WithAttrs(_ []slog.Attr) slog.Handler         { return h }
func (h *traceCapture) WithGroup(_ string) slog.Handler              { return h }
func (h *traceCapture) Handle(ctx context.Context, r slog.Record) error {
	attrs := make(map[string]any, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Resolve().Any()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, capturedLogRecord{ctx: ctx, level: r.Level, attrs: attrs})
	h.mu.Unlock()
	return nil
}

// traceIDInAnyRecord returns true if any captured record's context carries tid.
func (h *traceCapture) traceIDInAnyRecord(tid string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if linklog.TraceIDFromContext(r.ctx) == tid {
			return true
		}
	}
	return false
}

// unexpectedTraceIDs returns any non-empty context trace_id that is not tid.
func (h *traceCapture) unexpectedTraceIDs(tid string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := map[string]struct{}{}
	for _, r := range h.records {
		if got := linklog.TraceIDFromContext(r.ctx); got != "" && got != tid {
			seen[got] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

// warnWithAttr returns true if any captured WARN record has key=val in its attrs.
func (h *traceCapture) warnWithAttr(key, val string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.level != slog.LevelWarn {
			continue
		}
		if v, ok := r.attrs[key]; ok {
			if s, ok2 := v.(string); ok2 && s == val {
				return true
			}
		}
	}
	return false
}

// installTraceCapture installs a new traceCapture as the slog default for the
// duration of the test, restoring the previous default on cleanup.
func installTraceCapture(t *testing.T) *traceCapture {
	t.Helper()
	h := &traceCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

// enqueueWithEmptyTraceID enqueues a pending row then clears its trace_id,
// simulating a queue row written before EPIC-111 F3 added the trace_id column.
func enqueueWithEmptyTraceID(t *testing.T, q *Queue) int64 {
	t.Helper()
	id := enqueueCapturePending(t, q)
	if _, err := q.db.Exec(`UPDATE queue SET trace_id = '' WHERE id = ?`, id); err != nil {
		t.Fatalf("enqueueWithEmptyTraceID: clear trace_id: %v", err)
	}
	return id
}

var errTraceFetch = errors.New("trace_test_fetch_error")

// setFailingDomainRouter installs a DomainRouter whose only fetch path is a
// jina fallback that always errors. This forces FetchWithFallback to return an
// error regardless of the domain client, so captureAsync reaches the
// slog.ErrorContext("captureAsync: fetch failed") call after trace_id is wired.
func setFailingDomainRouter(t *testing.T, router *Router) {
	t.Helper()
	dr := NewDomainRouter(
		map[string]DomainClient{},
		func(_ context.Context, _ string) (string, error) { return "", errTraceFetch },
	)
	router.SetDomainRouter(dr)
}

// CT-1: slog records emitted inside captureAsync carry the queue row's trace_id in
// their context. Forces a total fetch failure (domain + jina) so captureAsync
// reaches slog.ErrorContext after trace_id is wired into ctx.
//
// Fails before M2: current slog.Error calls pass context.Background() to the handler;
// linklog.TraceIDFromContext(ctx) returns "" for all records.
func TestCaptureAsync_CT1_TraceIDPresentInSlogContext(t *testing.T) {
	h := installTraceCapture(t)
	s, q := newCaptureServer(t, nil)
	setFailingDomainRouter(t, s.router)
	s.RegisterCaptureRenderer("capture_jira_auto", &captureStubRenderer{})

	id := enqueueCapturePending(t, q)
	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("CT-1: GetByID: %v", err)
	}
	if item.TraceID == "" {
		t.Fatal("CT-1: pre-condition: enqueued item has empty trace_id")
	}

	s.wg.Add(1)
	go s.captureAsync(context.Background(), id, captureJiraAutoConfig(t.TempDir()))
	s.wg.Wait()

	if !h.traceIDInAnyRecord(item.TraceID) {
		t.Errorf("CT-1: no slog record carried trace_id %q in context", item.TraceID)
	}
}

// CT-2: the trace_id threaded into captureAsync slog context matches the queue row's
// trace_id exactly - it is not regenerated.
//
// Fails before M2: same root cause as CT-1.
func TestCaptureAsync_CT2_TraceIDMatchesQueueRow(t *testing.T) {
	h := installTraceCapture(t)
	s, q := newCaptureServer(t, nil)
	setFailingDomainRouter(t, s.router)
	s.RegisterCaptureRenderer("capture_jira_auto", &captureStubRenderer{})

	id := enqueueCapturePending(t, q)
	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("CT-2: GetByID: %v", err)
	}

	s.wg.Add(1)
	go s.captureAsync(context.Background(), id, captureJiraAutoConfig(t.TempDir()))
	s.wg.Wait()

	if !h.traceIDInAnyRecord(item.TraceID) {
		t.Errorf("CT-2: slog context trace_id does not match queue row trace_id %q", item.TraceID)
	}
	if bad := h.unexpectedTraceIDs(item.TraceID); len(bad) > 0 {
		t.Errorf("CT-2: slog context contained unexpected trace_ids %v (want only %q)", bad, item.TraceID)
	}
}

// RG-1: named regression guard for the CT-1 invariant. Any future change that drops
// WithTraceID or reverts slog calls to non-context form will be caught here by name.
func TestCaptureAsync_RG1_TraceIDRegressionGuard(t *testing.T) {
	h := installTraceCapture(t)
	s, q := newCaptureServer(t, nil)
	setFailingDomainRouter(t, s.router)
	s.RegisterCaptureRenderer("capture_jira_auto", &captureStubRenderer{})

	id := enqueueCapturePending(t, q)
	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("RG-1: GetByID: %v", err)
	}

	s.wg.Add(1)
	go s.captureAsync(context.Background(), id, captureJiraAutoConfig(t.TempDir()))
	s.wg.Wait()

	// Invariant: at least one slog record emitted by captureAsync must carry the
	// queue row's trace_id in its context. Regression: captureAsync dropped
	// linklog.WithTraceID or reverted to context-free slog.Error calls.
	if !h.traceIDInAnyRecord(item.TraceID) {
		t.Errorf("RG-1 regression: captureAsync slog context missing trace_id %q  -  "+
			"check linklog.WithTraceID call and slog.ErrorContext usage in captureAsync",
			item.TraceID)
	}
}

// CT-3: captureAsync with a pre-migration row (trace_id="") emits a WARN log with
// error_class=trace_id_absent_in_capture and completes without panic.
// The warning fires before any fetch attempt, so the domain router setup is immaterial.
//
// Fails before M2: no warning is emitted for empty trace_id.
func TestCaptureAsync_CT3_EmptyTraceIDEmitsWarning(t *testing.T) {
	h := installTraceCapture(t)
	s, q := newCaptureServer(t, nil)
	setFailingDomainRouter(t, s.router)
	s.RegisterCaptureRenderer("capture_jira_auto", &captureStubRenderer{})

	id := enqueueWithEmptyTraceID(t, q)

	s.wg.Add(1)
	go s.captureAsync(context.Background(), id, captureJiraAutoConfig(t.TempDir()))
	s.wg.Wait()

	if !h.warnWithAttr("error_class", "trace_id_absent_in_capture") {
		t.Error("CT-3: expected WARN record with error_class=trace_id_absent_in_capture, not found")
	}
}
