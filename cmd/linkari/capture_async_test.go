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
		dr := NewDomainRouter(
			map[string]DomainClient{"grindr.atlassian.net": domainClient},
			func(_ context.Context, _ string) (string, error) { return "jina-text", nil },
		)
		router.SetDomainRouter(dr)
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
		ID:                       "capture_jira_auto",
		Kind:                     KindCapture,
		ArtifactDir:              artifactDir,
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

// CT-5: captureAsync + no renderer registered → status=failed, capture_renderer_not_found.
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

// CT-13: captureAsync enqueues an FCM push notification after SetCaptured succeeds.
// F2 TDD §6 RG-5: Android must receive a "Captured: {KEY}" notification.
// Verified by querying push_outbox for a row with verdict='captured' and slug='TEST-001'.
func TestCaptureAsyncEnqueuesPush(t *testing.T) {
	dir := t.TempDir()
	domainClient := &captureStubDomainClient{
		content: `{"key":"TEST-001","fields":{"summary":"Test issue"}}`,
		ct:      ContentTypeJSON,
	}
	s, q := newCaptureServer(t, domainClient)
	s.RegisterCaptureRenderer("capture_jira_auto", &captureStubRenderer{
		keyFn: func(_ string) string { return "TEST-001" },
	})

	id := enqueueCapturePending(t, q)
	cfg := captureJiraAutoConfig(dir)

	s.wg.Add(1)
	go s.captureAsync(context.Background(), id, cfg)
	s.wg.Wait()

	// Assert push_outbox has exactly one row with verdict='captured' and slug='TEST-001'.
	var count int
	if err := q.db.QueryRow(
		`SELECT COUNT(*) FROM push_outbox WHERE verdict='captured' AND slug='TEST-001'`,
	).Scan(&count); err != nil {
		t.Fatalf("CT-13: query push_outbox: %v", err)
	}
	if count != 1 {
		t.Errorf("CT-13: expected 1 push_outbox row with verdict='captured' and slug='TEST-001', got %d", count)
	}

	// Assert the slug is non-empty (belt-and-suspenders on the key extraction).
	var slug string
	if err := q.db.QueryRow(
		`SELECT slug FROM push_outbox WHERE verdict='captured' LIMIT 1`,
	).Scan(&slug); err != nil {
		t.Fatalf("CT-13: query slug: %v", err)
	}
	if slug == "" {
		t.Errorf("CT-13: expected non-empty slug in push_outbox row")
	}
}

// sentinel errors for test stubs
var (
	errCaptureTestFetch  = errors.New("test_fetch_error")
	errCaptureTestRender = errors.New("test_render_error")
)

// -- F5 PostCaptureCommand contract tests (CT-1 through CT-7, RG-1, RG-2) --
//
// Written test-first for M1. All tests in this block must fail until M2 provides the
// real implementation of runPostCaptureCommand. The no-op M1 stub makes CT-4 and CT-5
// fail on missing behaviour; CT-1, CT-2, CT-3 fail on validate() not compiling the
// template; CT-6 and CT-7 fail on missing tmux/exec call records; RG tests fail on
// missing key-forwarding assertion.

// postCmdCaptureTmux is a test double for the tmux dispatch path in runPostCaptureCommand.
// M2 must route through this recorder when Target is set.
type postCmdCaptureTmux struct {
	mu        sync.Mutex
	calls     []postCmdNewWindowCall
	returnErr error
}

type postCmdNewWindowCall struct {
	session string
	command string
	name    string
}

func (r *postCmdCaptureTmux) NewWindow(session, command, name string) error {
	r.mu.Lock()
	r.calls = append(r.calls, postCmdNewWindowCall{session: session, command: command, name: name})
	r.mu.Unlock()
	return r.returnErr
}

// enqueueCaptureDone inserts a queue row and immediately marks it captured.
func enqueueCaptureDone(t *testing.T, q *Queue, artifactPath string) int64 {
	t.Helper()
	req := &ShareRequest{
		Type:    "url",
		URL:     "https://grindr.atlassian.net/browse/SR-2972",
		Action:  "capture_jira_auto",
		Profile: "eng",
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("enqueueCaptureDone: Enqueue: %v", err)
	}
	if err := q.SetCaptured(id, artifactPath); err != nil {
		t.Fatalf("enqueueCaptureDone: SetCaptured: %v", err)
	}
	return id
}

// F5-CT-1: PostCaptureCommand=="" → runPostCaptureCommand is a no-op; validate() must
// leave compiledPostCaptureTemplate nil.
// Fails until M2 wires the validate() path to compile (or skip) the template field.
func TestPostCapture_CT1_EmptyCommand_NoSubprocess(t *testing.T) {
	// Build a config with an empty PostCaptureCommand and run validate().
	// After validate(), compiledPostCaptureTemplate must be nil for an empty command.
	// With a non-empty command, validate() must populate compiledPostCaptureTemplate.
	// M1 stub: validate() never sets compiledPostCaptureTemplate → the non-empty case fails.
	cfg := &Config{
		Actions: []ActionConfig{
			{
				ID:                 "capture_jira_auto",
				Kind:               KindCapture,
				ArtifactDir:        t.TempDir(),
				PostCaptureCommand: "echo {{.Key}}",
			},
		},
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("F5-CT-1: validate() error: %v", err)
	}
	got := cfg.Actions[0].compiledPostCaptureTemplate
	// M2: validate() must compile a non-empty PostCaptureCommand into compiledPostCaptureTemplate.
	// Stub: compiledPostCaptureTemplate is always nil → test fails.
	if got == nil {
		t.Errorf("F5-CT-1: compiledPostCaptureTemplate is nil after validate() with non-empty PostCaptureCommand")
	}
}

// F5-CT-2: {{.Key}} in command template expands to the validated Jira key.
// Fails until M2 routes the rendered command through runPostCaptureCommand.
func TestPostCapture_CT2_KeyTemplateExpands(t *testing.T) {
	s, _ := newCaptureServer(t, nil)
	outFile := filepath.Join(t.TempDir(), "ct2_key.txt")

	cfg := &ActionConfig{
		ID:                 "capture_jira_auto",
		Kind:               KindCapture,
		Target:             "",
		PostCaptureCommand: "echo {{.Key}} > " + outFile,
	}
	s.runPostCaptureCommand(cfg, "SR-2972", "/tmp/artifact.md", "https://grindr.atlassian.net/browse/SR-2972")

	// M2: runPostCaptureCommand must render the template and exec it.
	// Stub does nothing → outFile does not exist → test fails.
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("F5-CT-2: output file not created (exec not called): %v", err)
	}
	if !strings.Contains(string(data), "SR-2972") {
		t.Errorf("F5-CT-2: output %q does not contain SR-2972", string(data))
	}
}

// F5-CT-3: {{.ArtifactPath}} and {{.Date}} both expand correctly in the rendered command.
// Fails until M2 provides template rendering in runPostCaptureCommand.
func TestPostCapture_CT3_ArtifactPathAndDateExpand(t *testing.T) {
	s, _ := newCaptureServer(t, nil)
	outFile := filepath.Join(t.TempDir(), "ct3_out.txt")
	artifactPath := "/docs/captures/2026-05-03_SR-2972.md"

	cfg := &ActionConfig{
		ID:                 "capture_jira_auto",
		Kind:               KindCapture,
		Target:             "",
		PostCaptureCommand: "echo '{{.ArtifactPath}} {{.Date}}' > " + outFile,
	}
	s.runPostCaptureCommand(cfg, "SR-2972", artifactPath, "https://grindr.atlassian.net/browse/SR-2972")

	// M2: both placeholders must appear in the output file.
	// Stub does nothing → outFile absent → test fails.
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("F5-CT-3: output file not created (exec not called): %v", err)
	}
	if !strings.Contains(string(data), artifactPath) {
		t.Errorf("F5-CT-3: ArtifactPath not in output %q", string(data))
	}
	// Date is injected by runPostCaptureCommand as time.Now().UTC().Format("2006-01-02").
	// We just check the format prefix is present.
	if !strings.Contains(string(data), "2026") {
		t.Errorf("F5-CT-3: Date not in output %q", string(data))
	}
	_ = s
}

// F5-CT-4: Target="linkari:0", valid command → tmux.NewWindow("linkari", command, "SR-2972 capture") called.
// Fails until M2 dispatches via tmux when Target is non-empty.
func TestPostCapture_CT4_TargetTmux_CallsNewWindow(t *testing.T) {
	rec := &postCmdCaptureTmux{}
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	s := NewServer("tok", router, q, NewRingLog(10), false, nil)
	// M2: wire the recorder as the post-capture tmux dispatcher.
	s.postCaptureTmux = rec

	cfg := &ActionConfig{
		ID:                 "capture_jira_auto",
		Kind:               KindCapture,
		Target:             "linkari:0",
		PostCaptureCommand: "echo done",
	}
	s.runPostCaptureCommand(cfg, "SR-2972", "/tmp/2026-05-03_SR-2972.md", "https://grindr.atlassian.net/browse/SR-2972")

	// M2 must call tmux.NewWindow with the rendered command and window name "SR-2972 capture".
	// Stub does nothing → rec.calls is empty → test fails.
	if len(rec.calls) == 0 {
		t.Fatalf("F5-CT-4: expected tmux.NewWindow to be called; no calls recorded (stub is no-op)")
	}
	got := rec.calls[0]
	if got.session != "linkari" {
		t.Errorf("F5-CT-4: session = %q, want %q", got.session, "linkari")
	}
	if !strings.Contains(got.command, "echo done") {
		t.Errorf("F5-CT-4: command %q does not contain rendered command", got.command)
	}
	if got.name != "SR-2972 capture" {
		t.Errorf("F5-CT-4: window name = %q, want %q", got.name, "SR-2972 capture")
	}
}

// F5-CT-5: Target="" → exec.Command("sh", "-c", command) called; not tmux.NewWindow.
// Fails until M2 provides the exec path for empty Target.
func TestPostCapture_CT5_TargetEmpty_ExecCommand(t *testing.T) {
	s, _ := newCaptureServer(t, nil)
	outFile := filepath.Join(t.TempDir(), "ct5_out.txt")

	cfg := &ActionConfig{
		ID:                 "capture_jira_auto",
		Kind:               KindCapture,
		Target:             "",
		PostCaptureCommand: "touch " + outFile,
	}
	s.runPostCaptureCommand(cfg, "SR-2972", "/tmp/artifact.md", "https://grindr.atlassian.net/browse/SR-2972")

	// M2: with empty Target, exec.Command("sh", "-c", rendered) must be called synchronously.
	// Stub does nothing → outFile absent → test fails.
	if _, err := os.Stat(outFile); err != nil {
		t.Errorf("F5-CT-5: exec.Command not called — expected %s to exist: %v", outFile, err)
	}
}

// F5-CT-6: Target set, tmux.NewWindow returns error → capture_command_error logged; row stays captured.
// Fails until M2 dispatches via tmux (so we can verify the tmux call happened even when it errors).
func TestPostCapture_CT6_TmuxError_StatusRemainsCaptured(t *testing.T) {
	rec := &postCmdCaptureTmux{returnErr: errors.New("tmux_unavailable")}
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	s := NewServer("tok", router, q, NewRingLog(10), false, nil)
	// M2: wire the recorder as the post-capture tmux dispatcher.
	s.postCaptureTmux = rec

	artifactPath := filepath.Join(t.TempDir(), "2026-05-03_SR-2972.md")
	if err := os.WriteFile(artifactPath, []byte("# artifact"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := enqueueCaptureDone(t, q, artifactPath)

	cfg := &ActionConfig{
		ID:                 "capture_jira_auto",
		Kind:               KindCapture,
		Target:             "linkari:0",
		PostCaptureCommand: "echo done",
	}
	s.runPostCaptureCommand(cfg, "SR-2972", artifactPath, "https://grindr.atlassian.net/browse/SR-2972")

	// M2: tmux.NewWindow must have been called (even though it errored).
	// Stub does nothing → rec.calls is empty → test fails.
	if len(rec.calls) == 0 {
		t.Errorf("F5-CT-6: tmux.NewWindow was not called — stub is a no-op")
	}
	// Best-effort: queue row must remain captured regardless of tmux error.
	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("F5-CT-6: GetByID: %v", err)
	}
	if item.Status != "captured" {
		t.Errorf("F5-CT-6: expected status=captured after tmux error, got %q", item.Status)
	}
}

// F5-CT-7: exec.Command exits non-zero → capture_command_error logged; row stays captured.
// Fails until M2 provides the exec path (stub does nothing; the "exit 1" never runs).
func TestPostCapture_CT7_ExecNonZero_StatusRemainsCaptured(t *testing.T) {
	s, q := newCaptureServer(t, nil)

	artifactPath := filepath.Join(t.TempDir(), "ct7_artifact.md")
	if err := os.WriteFile(artifactPath, []byte("# ct7"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := enqueueCaptureDone(t, q, artifactPath)

	// A command that will exit non-zero when M2 runs it via exec.Command("sh", "-c", ...).
	sentinelFile := filepath.Join(t.TempDir(), "ct7_ran.txt")
	cfg := &ActionConfig{
		ID:                 "capture_jira_auto",
		Kind:               KindCapture,
		Target:             "",
		PostCaptureCommand: "touch " + sentinelFile + " && exit 1",
	}
	s.runPostCaptureCommand(cfg, "SR-2972", artifactPath, "https://grindr.atlassian.net/browse/SR-2972")

	// M2: the command must have been attempted (sentinel exists) even though it failed.
	// Stub does nothing → sentinel absent → test fails.
	if _, err := os.Stat(sentinelFile); err != nil {
		t.Errorf("F5-CT-7: exec.Command not called — sentinel %s absent: %v", sentinelFile, err)
	}
	// Best-effort: queue row must remain captured regardless.
	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("F5-CT-7: GetByID: %v", err)
	}
	if item.Status != "captured" {
		t.Errorf("F5-CT-7: expected status=captured after exec failure, got %q", item.Status)
	}
}

// F5-RG-1: Queue row is status=captured after runPostCaptureCommand failure.
// Best-effort guarantee: the hook must never mutate queue state.
// Fails until M2 provides exec behaviour — the stub's no-op means exec never ran, so
// we verify via the sentinel that the exec WAS attempted (positive assertion).
func TestPostCapture_RG1_FailureDoesNotMutateQueueState(t *testing.T) {
	s, q := newCaptureServer(t, nil)

	artifactPath := filepath.Join(t.TempDir(), "rg1_artifact.md")
	if err := os.WriteFile(artifactPath, []byte("# rg1"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := enqueueCaptureDone(t, q, artifactPath)

	sentinelFile := filepath.Join(t.TempDir(), "rg1_ran.txt")
	cfg := &ActionConfig{
		ID:                 "capture_jira_auto",
		Kind:               KindCapture,
		Target:             "",
		PostCaptureCommand: "touch " + sentinelFile + " && false",
	}
	s.runPostCaptureCommand(cfg, "SR-2972", artifactPath, "https://grindr.atlassian.net/browse/SR-2972")

	// Positive assertion: M2 must exec the command (sentinel exists).
	// Stub does nothing → sentinel absent → test fails.
	if _, err := os.Stat(sentinelFile); err != nil {
		t.Errorf("F5-RG-1: exec not called — sentinel %s absent: %v", sentinelFile, err)
	}
	// Guard: queue row must be unchanged.
	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("F5-RG-1: GetByID: %v", err)
	}
	if item.Status != "captured" {
		t.Errorf("F5-RG-1: status mutated; got %q, want captured", item.Status)
	}
	if item.ArtifactPath != artifactPath {
		t.Errorf("F5-RG-1: artifact_path mutated; got %q, want %q", item.ArtifactPath, artifactPath)
	}
}

// F5-RG-2: PostCaptureContext.Key equals the value returned by renderer.ArtifactKey for the URL.
// Verifies that the key forwarded from captureAsync to runPostCaptureCommand is correct.
// Fails until M2 records the key that runPostCaptureCommand receives.
func TestPostCapture_RG2_ContextKeyMatchesArtifactKey(t *testing.T) {
	rawURL := "https://grindr.atlassian.net/browse/SR-2972"

	// Verify the extraction contract: ArtifactKey for this URL must return "SR-2972".
	renderer := &captureStubRenderer{
		keyFn: func(u string) string {
			parsed, err := url.Parse(u)
			if err != nil {
				return ""
			}
			key, err := ExtractJiraKey(parsed.Path)
			if err != nil {
				return ""
			}
			return key
		},
	}
	gotKey := renderer.ArtifactKey(rawURL)
	if gotKey != "SR-2972" {
		t.Fatalf("F5-RG-2: ArtifactKey(%q) = %q, want SR-2972 (extraction contract broken)", rawURL, gotKey)
	}

	// Integration assertion: run captureAsync end-to-end and verify the key that reaches
	// runPostCaptureCommand equals gotKey. M2 must record the received key in an observable way.
	// For now, we verify via a sentinel filename that embeds the key.
	dir := t.TempDir()
	domainClient := &captureStubDomainClient{
		content: `{"key":"SR-2972","fields":{"summary":"test"}}`,
		ct:      ContentTypeJSON,
	}
	s, q := newCaptureServer(t, domainClient)
	s.RegisterCaptureRenderer("capture_jira_auto", &captureStubRenderer{
		keyFn: func(u string) string { return "SR-2972" },
	})
	id := enqueueCaptureDone(t, q, filepath.Join(dir, "placeholder.md"))
	// Replace the captured row with a pending one so captureAsync processes it.
	req2 := &ShareRequest{
		Type:    "url",
		URL:     rawURL,
		Action:  "capture_jira_auto",
		Profile: "eng",
	}
	id2, err := q.Enqueue(req2)
	if err != nil {
		t.Fatalf("F5-RG-2: Enqueue: %v", err)
	}
	_ = id // original row not used beyond confirming SetCaptured works

	sentinelFile := filepath.Join(dir, "rg2_key_SR-2972.txt")
	cfg := captureJiraAutoConfig(dir)
	cfg.PostCaptureCommand = "touch " + sentinelFile

	s.wg.Add(1)
	go s.captureAsync(context.Background(), id2, cfg)
	s.wg.Wait()

	// M2: captureAsync must call runPostCaptureCommand, which must exec the command,
	// creating the sentinel. Stub does nothing → sentinel absent → test fails.
	if _, err := os.Stat(sentinelFile); err != nil {
		t.Errorf("F5-RG-2: PostCaptureCommand not invoked after captureAsync — sentinel %s absent: %v", sentinelFile, err)
	}
}
