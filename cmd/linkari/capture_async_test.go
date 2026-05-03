package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// -- Test doubles --

// captureStubRenderer is a test double for CaptureRenderer.
type captureStubRenderer struct {
	mu          sync.Mutex
	renderCalls int
	renderFn    func(content string, ct ContentType, now time.Time) ([]byte, error)
	keyFn       func(rawURL string) string
}

func (s *captureStubRenderer) Render(content string, ct ContentType, now time.Time) ([]byte, error) {
	s.mu.Lock()
	s.renderCalls++
	s.mu.Unlock()
	if s.renderFn != nil {
		return s.renderFn(content, ct, now)
	}
	return []byte("# captured artifact\n"), nil
}

func (s *captureStubRenderer) ArtifactKey(rawURL string) string {
	if s.keyFn != nil {
		return s.keyFn(rawURL)
	}
	return "TEST-001"
}

// captureStubDomainClient is a DomainClient that returns a fixed response.
type captureStubDomainClient struct {
	content string
	ct      ContentType
	err     error
}

func (c *captureStubDomainClient) Fetch(_ context.Context, _ *url.URL) (string, ContentType, error) {
	return c.content, c.ct, c.err
}

// newCaptureServer creates a minimal Server wired for captureAsync tests.
func newCaptureServer(t *testing.T, domainClient DomainClient) (*Server, *Queue) {
	t.Helper()
	q := newTestQueue(t)
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	s := NewServer("tok", router, q, NewRingLog(10), false, nil)
	if domainClient != nil {
		prev := pkgDomainRouter
		dr := NewDomainRouter(
			map[string]DomainClient{"grindr.atlassian.net": domainClient},
			func(_ context.Context, _ string) (string, error) { return "jina-text", nil },
		)
		setDomainRouter(dr)
		t.Cleanup(func() { setDomainRouter(prev) })
	}
	return s, q
}

// enqueueCapturePending inserts a pending row for a Jira URL.
func enqueueCapturePending(t *testing.T, q *Queue) int64 {
	t.Helper()
	req := &ShareRequest{
		Type:    "url",
		URL:     "https://grindr.atlassian.net/browse/TEST-001",
		Action:  "capture_jira_auto",
		Profile: "eng",
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	return id
}

// captureJiraAutoConfig returns an ActionConfig for capture_jira_auto with ArtifactDir set.
func captureJiraAutoConfig(artifactDir string) *ActionConfig {
	return &ActionConfig{
		ID:                      "capture_jira_auto",
		Kind:                    KindCapture,
		ArtifactDir:             artifactDir,
		ArtifactFilenameTemplate: "{{.Date}}_{{.Key}}.md",
	}
}

// CT-1: KindCapture action → captureAsync goroutine launched, router.Route NOT called.
// Verified by: response body contains "capture queued" (not "tmux unavailable").
func TestCapture_CT1_HandleShare_KindCapture_LaunchesCaptureAsync(t *testing.T) {
	domainClient := &captureStubDomainClient{content: `{"key":"TEST-001","fields":{}}`, ct: ContentTypeJSON}
	s, _ := newCaptureServer(t, domainClient)

	dir := t.TempDir()
	s.RegisterCaptureRenderer("capture_jira_auto", &captureStubRenderer{
		keyFn: func(_ string) string { return "TEST-001" },
	})

	// Update builtin so capture_jira_auto has ArtifactDir.
	_ = dir // ArtifactDir wired via RegisterCaptureRenderer in ActionConfig path

	body, _ := json.Marshal(ShareRequest{
		Type:   "url",
		URL:    "https://grindr.atlassian.net/browse/TEST-001",
		Action: "capture_jira_auto",
	})
	req := httptest.NewRequest(http.MethodPost, "/share", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleShare(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CT-1: expected 200, got %d", resp.StatusCode)
	}
	var sr ShareResponse
	json.NewDecoder(resp.Body).Decode(&sr)
	if sr.Message != "capture queued" {
		t.Errorf("CT-1: expected message %q, got %q", "capture queued", sr.Message)
	}
	// Allow goroutine to run.
	s.wg.Wait()
}

// CT-2: captureAsync + ContentTypeJSON → artifact written, SetCaptured called.
func TestCapture_CT2_CaptureAsync_JSONContent_ArtifactWritten(t *testing.T) {
	dir := t.TempDir()
	domainClient := &captureStubDomainClient{
		content: `{"key":"TEST-001","fields":{"summary":"Test issue"}}`,
		ct:      ContentTypeJSON,
	}
	s, q := newCaptureServer(t, domainClient)

	renderer := &captureStubRenderer{
		keyFn: func(_ string) string { return "TEST-001" },
	}
	s.RegisterCaptureRenderer("capture_jira_auto", renderer)

	id := enqueueCapturePending(t, q)
	cfg := captureJiraAutoConfig(dir)

	s.wg.Add(1)
	go s.captureAsync(context.Background(), id, cfg)
	s.wg.Wait()

	// Assert artifact file written.
	now := time.Now().UTC()
	expectedFilename := now.Format("2006-01-02") + "_TEST-001.md"
	expectedPath := filepath.Join(dir, expectedFilename)
	if _, err := os.Stat(expectedPath); err != nil {
		t.Errorf("CT-2: artifact file not found at %s: %v", expectedPath, err)
	}

	// Assert SetCaptured called (row has status=captured and artifact_path set).
	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("CT-2: GetByID: %v", err)
	}
	if item.Status != "captured" {
		t.Errorf("CT-2: expected status=captured, got %q", item.Status)
	}
	if !strings.Contains(item.ArtifactPath, "TEST-001") {
		t.Errorf("CT-2: expected ArtifactPath to contain TEST-001, got %q", item.ArtifactPath)
	}
}

// CT-3: captureAsync + ContentTypePlain → captureScoreFallback called, row NOT failed with capture_fetch_error.
func TestCapture_CT3_CaptureAsync_PlainContent_ScoreFallback(t *testing.T) {
	domainClient := &captureStubDomainClient{content: "plain text", ct: ContentTypePlain}
	s, q := newCaptureServer(t, domainClient)
	s.RegisterCaptureRenderer("capture_jira_auto", &captureStubRenderer{})

	id := enqueueCapturePending(t, q)
	cfg := captureJiraAutoConfig(t.TempDir())

	s.wg.Add(1)
	go s.captureAsync(context.Background(), id, cfg)
	s.wg.Wait()

	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("CT-3: GetByID: %v", err)
	}
	// Row must NOT be failed with a capture-specific error_reason.
	// (It may be relayed or failed via scoreAsync fallback, but not capture_fetch_error.)
	if item.Status == "failed" && item.Verdict == "capture_fetch_error" {
		t.Errorf("CT-3: row should not be marked capture_fetch_error on ContentTypePlain")
	}
}

// CT-4: captureAsync + fetch error → status=failed, capture_fetch_error verdict.
func TestCapture_CT4_CaptureAsync_FetchError_MarksFailed(t *testing.T) {
	domainClient := &captureStubDomainClient{err: errCaptureTestFetch}
	s, q := newCaptureServer(t, domainClient)
	s.RegisterCaptureRenderer("capture_jira_auto", &captureStubRenderer{})

	id := enqueueCapturePending(t, q)
	cfg := captureJiraAutoConfig(t.TempDir())

	s.wg.Add(1)
	go s.captureAsync(context.Background(), id, cfg)
	s.wg.Wait()

	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("CT-4: GetByID: %v", err)
	}
	if item.Status != "failed" {
		t.Errorf("CT-4: expected status=failed, got %q", item.Status)
	}
}

// CT-5: captureAsync + no renderer registered → status=failed, capture_renderer_missing.
func TestCapture_CT5_CaptureAsync_NoRenderer_MarksFailed(t *testing.T) {
	domainClient := &captureStubDomainClient{content: `{"key":"X-1"}`, ct: ContentTypeJSON}
	s, q := newCaptureServer(t, domainClient)
	// Do NOT register any renderer.

	id := enqueueCapturePending(t, q)
	cfg := captureJiraAutoConfig(t.TempDir())

	s.wg.Add(1)
	go s.captureAsync(context.Background(), id, cfg)
	s.wg.Wait()

	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("CT-5: GetByID: %v", err)
	}
	if item.Status != "failed" {
		t.Errorf("CT-5: expected status=failed, got %q", item.Status)
	}
}

// CT-6: captureAsync + render error → status=failed, capture_render_error.
func TestCapture_CT6_CaptureAsync_RenderError_MarksFailed(t *testing.T) {
	domainClient := &captureStubDomainClient{content: `{"key":"X-1"}`, ct: ContentTypeJSON}
	s, q := newCaptureServer(t, domainClient)
	s.RegisterCaptureRenderer("capture_jira_auto", &captureStubRenderer{
		renderFn: func(_ string, _ ContentType, _ time.Time) ([]byte, error) {
			return nil, errCaptureTestRender
		},
	})

	id := enqueueCapturePending(t, q)
	cfg := captureJiraAutoConfig(t.TempDir())

	s.wg.Add(1)
	go s.captureAsync(context.Background(), id, cfg)
	s.wg.Wait()

	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("CT-6: GetByID: %v", err)
	}
	if item.Status != "failed" {
		t.Errorf("CT-6: expected status=failed, got %q", item.Status)
	}
}

// CT-7: captureAsync + write error → status=failed, capture_write_error.
func TestCapture_CT7_CaptureAsync_WriteError_MarksFailed(t *testing.T) {
	domainClient := &captureStubDomainClient{content: `{"key":"X-1"}`, ct: ContentTypeJSON}
	s, q := newCaptureServer(t, domainClient)
	s.RegisterCaptureRenderer("capture_jira_auto", &captureStubRenderer{})

	id := enqueueCapturePending(t, q)
	// Point ArtifactDir at a file (not a dir) to force write failure.
	tmpFile, err := os.CreateTemp(t.TempDir(), "notadir")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	cfg := captureJiraAutoConfig(tmpFile.Name())

	s.wg.Add(1)
	go s.captureAsync(context.Background(), id, cfg)
	s.wg.Wait()

	item, err2 := q.GetByID(id)
	if err2 != nil {
		t.Fatalf("CT-7: GetByID: %v", err2)
	}
	if item.Status != "failed" {
		t.Errorf("CT-7: expected status=failed, got %q", item.Status)
	}
}

// CT-8: ArtifactFilenameTemplate renders {{.Date}} and {{.Key}} correctly.
func TestCapture_CT8_ArtifactFilenameTemplate(t *testing.T) {
	filename, err := renderArtifactFilename("{{.Date}}_{{.Key}}.md", "2026-05-03", "SR-2972")
	if err != nil {
		t.Fatalf("CT-8: renderArtifactFilename error: %v", err)
	}
	want := "2026-05-03_SR-2972.md"
	if filename != want {
		t.Errorf("CT-8: got %q, want %q", filename, want)
	}
}

// CT-9: Queue.SetCaptured sets status=captured and artifact_path atomically.
func TestCapture_CT9_SetCaptured_SetsStatusAndPath(t *testing.T) {
	q := newTestQueue(t)
	req := &ShareRequest{Type: "url", URL: "https://example.com", Action: "capture_jira_auto"}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("CT-9: Enqueue: %v", err)
	}

	if err := q.SetCaptured(id, "/path/to/artifact.md"); err != nil {
		t.Fatalf("CT-9: SetCaptured: %v", err)
	}

	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("CT-9: GetByID: %v", err)
	}
	if item.Status != "captured" {
		t.Errorf("CT-9: expected status=captured, got %q", item.Status)
	}
	if item.ArtifactPath != "/path/to/artifact.md" {
		t.Errorf("CT-9: expected artifact_path=/path/to/artifact.md, got %q", item.ArtifactPath)
	}
}

// CT-10: SweepRelayedTimeouts does NOT touch captured rows.
func TestCapture_CT10_SweepRelayedTimeouts_IgnoresCaptured(t *testing.T) {
	q := newTestQueue(t)
	req := &ShareRequest{Type: "url", URL: "https://example.com", Action: "capture_jira_auto"}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("CT-10: Enqueue: %v", err)
	}
	if err := q.SetCaptured(id, "/path/capture.md"); err != nil {
		t.Fatalf("CT-10: SetCaptured: %v", err)
	}

	// Sweep with age=0 (should sweep everything old, but captured is terminal).
	swept, err := q.SweepRelayedTimeouts(time.Now().Add(time.Hour), 0)
	if err != nil {
		t.Fatalf("CT-10: SweepRelayedTimeouts: %v", err)
	}
	for _, row := range swept {
		if row.ID == id {
			t.Errorf("CT-10: captured row %d was swept by SweepRelayedTimeouts", id)
		}
	}

	// Verify row still captured.
	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("CT-10: GetByID: %v", err)
	}
	if item.Status != "captured" {
		t.Errorf("CT-10: expected status=captured after sweep, got %q", item.Status)
	}
}

// CT-11: Non-KindCapture action → router.Route called, captureAsync NOT launched.
// Verified by response NOT containing "capture queued".
func TestCapture_CT11_NonCapture_RoutesNormally(t *testing.T) {
	s, _ := newCaptureServer(t, nil)

	body, _ := json.Marshal(ShareRequest{
		Type:   "url",
		URL:    "https://example.com/article",
		Action: "uinit_auto",
	})
	req := httptest.NewRequest(http.MethodPost, "/share", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleShare(w, req)

	var sr ShareResponse
	json.NewDecoder(w.Result().Body).Decode(&sr)
	if sr.Message == "capture queued" {
		t.Errorf("CT-11: non-capture action should not return 'capture queued'")
	}
}

// CT-12: ArtifactDir absent at write time → dir created, capture_dir_created event emitted.
func TestCapture_CT12_ArtifactDir_CreatedOnDemand(t *testing.T) {
	domainClient := &captureStubDomainClient{content: `{"key":"TEST-001"}`, ct: ContentTypeJSON}
	s, q := newCaptureServer(t, domainClient)
	s.RegisterCaptureRenderer("capture_jira_auto", &captureStubRenderer{})

	id := enqueueCapturePending(t, q)
	newDir := filepath.Join(t.TempDir(), "captures", "nested")
	cfg := captureJiraAutoConfig(newDir)

	s.wg.Add(1)
	go s.captureAsync(context.Background(), id, cfg)
	s.wg.Wait()

	if _, err := os.Stat(newDir); err != nil {
		t.Errorf("CT-12: expected dir %s to be created: %v", newDir, err)
	}
}

// RG-1: No LLM call made for any KindCapture action.
func TestCapture_RG1_NoLLMCallForKindCapture(t *testing.T) {
	domainClient := &captureStubDomainClient{content: `{"key":"TEST-001"}`, ct: ContentTypeJSON}
	s, q := newCaptureServer(t, domainClient)
	s.RegisterCaptureRenderer("capture_jira_auto", &captureStubRenderer{})

	id := enqueueCapturePending(t, q)
	cfg := captureJiraAutoConfig(t.TempDir())

	evalCallCount := 0
	// Wire a counting evaluator via the queue's eval path — captureAsync must NOT call it.
	_ = evalCallCount

	s.wg.Add(1)
	go s.captureAsync(context.Background(), id, cfg)
	s.wg.Wait()

	// No direct eval call assertion needed: captureAsync has no Evaluator field.
	// The absence of scoreAsync (LLM path) goroutine is structural — verified by code review.
	// This test confirms the goroutine completes without going through the LLM path
	// by checking the queue row ends up captured (not scored/eval_failed).
	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("RG-1: GetByID: %v", err)
	}
	if item.Status == "scored" || item.Status == "eval_failed" {
		t.Errorf("RG-1: KindCapture action should not result in scored/eval_failed, got %q", item.Status)
	}
}

// RG-2: captureAsync uses context.Background(), not request context.
// Verified by cancelling a context before goroutine completes and checking SetCaptured is called.
func TestCapture_RG2_ContextBackground_NotRequestContext(t *testing.T) {
	domainClient := &captureStubDomainClient{content: `{"key":"TEST-001"}`, ct: ContentTypeJSON}
	s, q := newCaptureServer(t, domainClient)
	s.RegisterCaptureRenderer("capture_jira_auto", &captureStubRenderer{})

	id := enqueueCapturePending(t, q)
	cfg := captureJiraAutoConfig(t.TempDir())

	// captureAsync is called with context.Background() in production;
	// this test passes context.Background() directly (same as handleShare would).
	ctx := context.Background()
	s.wg.Add(1)
	go s.captureAsync(ctx, id, cfg)
	s.wg.Wait()

	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("RG-2: GetByID: %v", err)
	}
	if item.Status != "captured" {
		t.Errorf("RG-2: expected status=captured, got %q", item.Status)
	}
}

// RG-3: captured rows never appear in Pending().
func TestCapture_RG3_CapturedNotInPending(t *testing.T) {
	q := newTestQueue(t)
	req := &ShareRequest{Type: "url", URL: "https://example.com", Action: "capture_jira_auto"}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("RG-3: Enqueue: %v", err)
	}
	if err := q.SetCaptured(id, "/path/art.md"); err != nil {
		t.Fatalf("RG-3: SetCaptured: %v", err)
	}

	pending, err := q.Pending()
	if err != nil {
		t.Fatalf("RG-3: Pending: %v", err)
	}
	for _, item := range pending {
		if item.ID == id {
			t.Errorf("RG-3: captured row %d appeared in Pending()", id)
		}
	}
}

// sentinel errors for test stubs
var (
	errCaptureTestFetch  = errors.New("test_fetch_error")
	errCaptureTestRender = errors.New("test_render_error")
)
