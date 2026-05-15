package main

// EPIC-001 M1: Pipeline contract invariant tests.
//
// These tests assert the desired behaviour of the scoring pipeline:
//   1. scoreAsync always terminates with a terminal queue-row status
//      (scored, failed, or archived) — never stuck in pending or relayed.
//   2. processVoiceNoteAsync always terminates with a terminal status.
//   3. HaikuVisionEvaluator double failure returns (nil, error), not a
//      synthetic scorecard — contract enforced by EPIC-001 M2 fix.
//   4. A successful /share request (HTTP 200, non-prefiltered) always creates
//      a queue row — the share→queue row guarantee.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// terminalStatuses is the set of valid terminal queue row statuses.
var terminalStatuses = map[string]bool{
	"scored":   true,
	"failed":   true,
	"archived": true,
}

// installTestProfileDir creates a minimal profile YAML for the given profile
// name in a temp dir and sets ORG_PATH so loadProfileTemplateJSON picks it up.
// Returns the temp base dir.
func installTestProfileDir(t *testing.T, profileName string) string {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "docs", "prompts", "profiles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdirall profile dir: %v", err)
	}
	manifest := `id: ` + profileName + `
version: 1
schema_version: triage_verdict_v1
persona_intro: "You are a triage assistant."
noise_gate:
  min_chars: 10
  skip_label: "too short"
persona_body: |
  ## Task
  Evaluate this content.
verdict_prompt: "summarize"
rubric:
  - name: Relevance
    weight: 20
    rationale: "relevant?"
  - name: Depth
    weight: 20
    rationale: "deep?"
  - name: Novelty
    weight: 20
    rationale: "novel?"
  - name: Clarity
    weight: 20
    rationale: "clear?"
  - name: Actionability
    weight: 20
    rationale: "actionable?"
action_items:
  count: "1-3"
  horizon_days: 7
key_facts:
  count: "2-4"
`
	if err := os.WriteFile(filepath.Join(dir, profileName+".yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write profile yaml: %v", err)
	}
	t.Setenv("ORG_PATH", base)
	return base
}

// isTerminal returns true if the given queue status is a pipeline terminal state.
func isTerminal(status string) bool {
	return terminalStatuses[status]
}

// ─── M1.1: scoreAsync terminal status ─────────────────────────────────────────

// TestScoreAsyncTerminalStatus verifies that every code path through scoreAsync
// exits with the queue row in a terminal status (scored, failed, or archived).
// Rows must never be left in pending or relayed after scoreAsync returns.
func TestScoreAsyncTerminalStatus(t *testing.T) {
	type testCase struct {
		name     string
		jinaCode int
		jinaBody string
		// evalErr overrides stubEvaluator.err when non-nil.
		evalErr error
	}
	cases := []testCase{
		{
			name:     "fetch_error",
			jinaCode: http.StatusInternalServerError,
			jinaBody: "",
		},
		{
			name:     "empty_content",
			jinaCode: http.StatusOK,
			jinaBody: "   ",
		},
		{
			name:     "eval_failure",
			jinaCode: http.StatusOK,
			jinaBody: "excellent content about engineering and transformers",
			evalErr:  fmt.Errorf("stubbed eval error"),
		},
		{
			name:     "happy_path",
			jinaCode: http.StatusOK,
			jinaBody: "excellent content about engineering and transformers",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateEventsDir(t)
			installTestProfileDir(t, "eng")

			srv := jinaBodyServer(t, tc.jinaCode, tc.jinaBody)
			installJinaServer(t, srv)

			// Stub content classify so the cascade resolves without Haiku.
			prevCC := execContentClassify
			execContentClassify = func(_ context.Context, _, _ string) (string, error) {
				return "eng", nil
			}
			t.Cleanup(func() { execContentClassify = prevCC })

			q := newTestQueue(t)
			q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

			req := &ShareRequest{
				Type:    "url",
				URL:     "https://example.com/paper",
				Profile: "eng",
			}
			id, err := q.Enqueue(req)
			if err != nil {
				t.Fatalf("Enqueue: %v", err)
			}
			if err := q.MarkRelayed(id); err != nil {
				t.Fatalf("MarkRelayed: %v", err)
			}
			req.QueueRowID = id

			inner := &stubEvaluator{score: 85, verdict: "Worth reading"}
			if tc.evalErr != nil {
				inner = &stubEvaluator{err: tc.evalErr}
			}
			done := make(chan struct{})
			wrapped := &onceDoneEval{inner: inner, done: done}

			go scoreAsync(req, q, wrapped, nil, nil)
			select {
			case <-done:
				// Eval was called — give goroutine time to finish post-eval work.
				time.Sleep(100 * time.Millisecond)
			case <-time.After(3 * time.Second):
				// Early-exit path (fetch error, empty content) — eval never reached.
				time.Sleep(100 * time.Millisecond)
			}

			var status string
			if err := q.db.QueryRow("SELECT status FROM queue WHERE id=?", id).Scan(&status); err != nil {
				t.Fatalf("query row %d: %v", id, err)
			}
			if !isTerminal(status) {
				t.Errorf("case %q: queue row status = %q, want one of scored/failed/archived", tc.name, status)
			}
		})
	}
}

// ─── M1.2: processVoiceNoteAsync terminal status ───────────────────────────────

// TestProcessVoiceNoteAsyncTerminalStatus verifies that every code path through
// processVoiceNoteAsync exits with the queue row in a terminal status.
func TestProcessVoiceNoteAsyncTerminalStatus(t *testing.T) {
	type testCase struct {
		name        string
		ffmpegErr   error
		whisperTx   string
		whisperErr  error
		wantStatus  []string
	}
	cases := []testCase{
		{
			name:       "ffmpeg_failure",
			ffmpegErr:  fmt.Errorf("ffmpeg: no such file"),
			wantStatus: []string{"failed"},
		},
		{
			name:       "whisper_failure",
			ffmpegErr:  nil,
			whisperErr: fmt.Errorf("whisper crashed"),
			wantStatus: []string{"failed"},
		},
		{
			name:       "empty_transcript",
			ffmpegErr:  nil,
			whisperTx:  "",
			wantStatus: []string{"failed"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Install ffmpeg stub.
			prevFfmpeg := execFfmpegConvert
			if tc.ffmpegErr != nil {
				execFfmpegConvert = func(_ context.Context, _, _ string) error {
					return tc.ffmpegErr
				}
			} else {
				execFfmpegConvert = func(_ context.Context, _, outputPath string) error {
					return os.WriteFile(outputPath, []byte("RIFF-fake-wav"), 0o644)
				}
			}
			t.Cleanup(func() { execFfmpegConvert = prevFfmpeg })

			// Install whisper stub (only relevant if ffmpeg succeeds).
			if tc.ffmpegErr == nil {
				installWhisperStub(t, tc.whisperTx, tc.whisperErr)
			}

			// Create a dummy audio file.
			audioPath := filepath.Join(t.TempDir(), "test.m4a")
			if err := os.WriteFile(audioPath, []byte("FAKE-M4A"), 0o644); err != nil {
				t.Fatalf("write audio: %v", err)
			}

			q := newTestQueue(t)
			req := &ShareRequest{Type: "audio", MimeType: "audio/m4a", Profile: "life"}
			id, err := q.Enqueue(req)
			if err != nil {
				t.Fatalf("Enqueue: %v", err)
			}
			req.QueueRowID = id

			finished := make(chan struct{})
			go func() {
				defer close(finished)
				processVoiceNoteAsync(audioPath, "life", q, id, "test.m4a", "", "", req, nil, &stubEvaluator{score: 80, verdict: "interesting"})
			}()

			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Error("processVoiceNoteAsync timed out")
			}

			var status string
			if err := q.db.QueryRow("SELECT status FROM queue WHERE id=?", id).Scan(&status); err != nil {
				t.Fatalf("query row %d: %v", id, err)
			}
			if !isTerminal(status) {
				t.Errorf("case %q: queue row status = %q, want one of scored/failed/archived", tc.name, status)
			}
			// All failure cases should specifically be "failed".
			for _, want := range tc.wantStatus {
				if status == want {
					return
				}
			}
			t.Errorf("case %q: status = %q, want one of %v", tc.name, status, tc.wantStatus)
		})
	}
}

// ─── M1.3: HaikuVisionEvaluator double-failure contract ──────────────────────

// TestHaikuVisionDoubleFailureReturnsError verifies that when both the vision
// exec path AND the JSON fallback fail, HaikuVisionEvaluator.Evaluate returns
// (nil, non-nil error) — not a synthetic (scorecard, nil) with a metadata-only
// result. This is the contract enforced by the EPIC-001 M2 fix in evaluator.go.
//
// Before M2: returns (&Scorecard{Verdict:"eval_failed", Backend:"metadata-only"}, nil)
// After  M2: returns (nil, error)
func TestHaikuVisionDoubleFailureReturnsError(t *testing.T) {
	// Stub vision exec to fail.
	prevVision := runClaudeHaikuVision
	runClaudeHaikuVision = func(_ context.Context, _, _, _, _ string) ([]byte, error) {
		return nil, fmt.Errorf("vision: exec crashed")
	}
	t.Cleanup(func() { runClaudeHaikuVision = prevVision })

	// Stub JSON fallback to also fail — both paths are dead.
	prevJSON := execHaikuJSON
	execHaikuJSON = func(_ context.Context, _, _, _ string) ([]byte, error) {
		return nil, fmt.Errorf("json: eval also crashed")
	}
	t.Cleanup(func() { execHaikuJSON = prevJSON })

	tmpFile := filepath.Join(t.TempDir(), "test.jpg")
	if err := os.WriteFile(tmpFile, []byte("fake-image-data"), 0o644); err != nil {
		t.Fatalf("write temp image: %v", err)
	}

	e := HaikuVisionEvaluator{ImagePath: tmpFile}
	sc, err := e.Evaluate(context.Background(), "some metadata text", "score this image")

	// Contract: double failure MUST propagate an error, not swallow it with a
	// synthetic scorecard. The caller (scoreAsync) must decide what to do on
	// error — returning a zero-score/eval_failed scorecard hides the failure.
	if err == nil {
		t.Errorf("Evaluate returned nil error on double failure; sc = %+v (want error)", sc)
	}
	if sc != nil {
		t.Errorf("Evaluate returned non-nil scorecard on double failure: backend=%q verdict=%q (want nil)", sc.Backend, sc.Verdict)
	}
}

// ─── M6.1: classifySkipReason eval_failed case ────────────────────────────────

// TestClassifySkipReason_EvalFailed verifies that the "eval_failed" verdict
// returns the dedicated "eval_failed" skip tag — not the generic "skipped".
// EPIC-001 M2 adds the explicit case; this test pins the invariant.
func TestClassifySkipReason_EvalFailed(t *testing.T) {
	got := classifySkipReason(0, "eval_failed")
	if got != "eval_failed" {
		t.Errorf("classifySkipReason(0, %q) = %q, want %q", "eval_failed", got, "eval_failed")
	}
}

// TestClassifySkipReason_EvalFailedNonZeroScore verifies that a non-zero score
// always returns "" regardless of verdict (scored items are never "skipped").
func TestClassifySkipReason_EvalFailedNonZeroScore(t *testing.T) {
	got := classifySkipReason(50, "eval_failed")
	if got != "" {
		t.Errorf("classifySkipReason(50, %q) = %q, want %q", "eval_failed", got, "")
	}
}

// ─── M6.2: vision double-failure marks row failed ────────────────────────────

// TestVisionDoubleFailure_MarksFailedNotScored verifies the full pipeline
// consequence of HaikuVisionEvaluator double failure: the queue row ends up
// with status=failed, not scored=0.
func TestVisionDoubleFailure_MarksFailedNotScored(t *testing.T) {
	isolateEventsDir(t)
	installTestProfileDir(t, "life")

	// Both vision and JSON eval fail.
	prevVision := runClaudeHaikuVision
	runClaudeHaikuVision = func(_ context.Context, _, _, _, _ string) ([]byte, error) {
		return nil, fmt.Errorf("vision crashed")
	}
	t.Cleanup(func() { runClaudeHaikuVision = prevVision })

	prevJSON := execHaikuJSON
	execHaikuJSON = func(_ context.Context, _, _, _ string) ([]byte, error) {
		return nil, fmt.Errorf("json also crashed")
	}
	t.Cleanup(func() { execHaikuJSON = prevJSON })

	tmpFile := filepath.Join(t.TempDir(), "img.jpg")
	if err := os.WriteFile(tmpFile, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}

	q := newTestQueue(t)
	req := &ShareRequest{
		Type:      "image",
		Filename:  "img.jpg",
		MimeType:  "image/jpeg",
		Profile:   "life",
		AudioPath: tmpFile,
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := q.MarkRelayed(id); err != nil {
		t.Fatalf("MarkRelayed: %v", err)
	}
	req.QueueRowID = id

	done := make(chan struct{})
	wrapped := &onceDoneEval{inner: HaikuVisionEvaluator{ImagePath: tmpFile}, done: done}
	go scoreAsync(req, q, wrapped, nil, nil)
	select {
	case <-done:
		time.Sleep(100 * time.Millisecond)
	case <-time.After(3 * time.Second):
		time.Sleep(100 * time.Millisecond)
	}

	var status string
	if err := q.db.QueryRow("SELECT status FROM queue WHERE id=?", id).Scan(&status); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "failed" {
		t.Errorf("status = %q, want failed (double-failure must mark row failed, not scored)", status)
	}
}

// ─── M6.3: YouTube audio fallback test ──────────────────────────────────────

// TestTranscribeYouTubeAsync_NoSubtitlesFallback verifies that when subtitle
// extraction fails with yt_no_subtitles and ytFallbackToAudio is enabled, the
// pipeline falls back to audio download → ffmpeg → whisper and marks the row
// as scored (transcript available) rather than failed.
func TestTranscribeYouTubeAsync_NoSubtitlesFallback(t *testing.T) {
	isolateEventsDir(t)

	// Enable the fallback gate for this test.
	prev := ytFallbackToAudio
	ytFallbackToAudio = true
	t.Cleanup(func() { ytFallbackToAudio = prev })

	// Subtitle extraction always fails with "no subtitles".
	prevYtdlp := execYtdlp
	execYtdlp = func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		return "", ytVideoMeta{Title: "Test Video"}, fmt.Errorf("no subtitles found for url")
	}
	t.Cleanup(func() { execYtdlp = prevYtdlp })

	// Audio download succeeds — returns a fake audio file path.
	fakeAudio := filepath.Join(t.TempDir(), "audio.m4a")
	if err := os.WriteFile(fakeAudio, []byte("FAKE-M4A"), 0o644); err != nil {
		t.Fatalf("write fake audio: %v", err)
	}
	prevAudio := execYtdlpAudio
	execYtdlpAudio = func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		return fakeAudio, ytVideoMeta{Title: "Test Video", ID: "abc123"}, nil
	}
	t.Cleanup(func() { execYtdlpAudio = prevAudio })

	// ffmpeg converts audio to wav.
	prevFfmpeg := execFfmpegConvert
	execFfmpegConvert = func(_ context.Context, _, outputPath string) error {
		return os.WriteFile(outputPath, []byte("RIFF-fake-wav"), 0o644)
	}
	t.Cleanup(func() { execFfmpegConvert = prevFfmpeg })

	// Whisper transcribes successfully.
	installWhisperStub(t, "This is a transcribed YouTube video about machine learning.", nil)

	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})
	req := ShareRequest{
		Type:    "url",
		URL:     "https://youtube.com/watch?v=abc123",
		Profile: "eng",
	}
	id, err := q.Enqueue(&req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := q.MarkRelayed(id); err != nil {
		t.Fatalf("MarkRelayed: %v", err)
	}
	req.QueueRowID = id

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		transcribeYouTubeAsync(req, q, "yt-dlp", nil, "", nil)
	}()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Error("transcribeYouTubeAsync timed out")
	}

	var status string
	if err := q.db.QueryRow("SELECT status FROM queue WHERE id=?", id).Scan(&status); err != nil {
		t.Fatalf("query: %v", err)
	}
	// transcribeYouTubeAsync doesn't score — it writes transcript and marks via
	// EnqueueTranscriptPush. The row should not be "failed".
	if status == "failed" {
		t.Errorf("status = failed after audio fallback — expected the fallback to succeed and not mark row failed")
	}
}

// ─── M1.4: Share endpoint → queue row guarantee ───────────────────────────────

// TestShareEndpointQueueRowGuarantee verifies that a successful share request
// (HTTP 200, not pre-filtered) always creates a persisted queue row. This is
// the share→queue row invariant: every accepted share must be auditable.
func TestShareEndpointQueueRowGuarantee(t *testing.T) {
	isolateEventsDir(t)
	installTestProfileDir(t, "eng")

	// Stub Jina to prevent the scoring goroutine from reaching the network.
	jina := jinaBodyServer(t, http.StatusOK, "interesting engineering content")
	installJinaServer(t, jina)

	// Stub content classify so async scoring resolves without Haiku.
	prevCC := execContentClassify
	execContentClassify = func(_ context.Context, _, _ string) (string, error) {
		return "eng", nil
	}
	t.Cleanup(func() { execContentClassify = prevCC })

	cfg := builtinConfig()
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, cfg, false)
	q := newTestQueue(t)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)
	mux := srv.Mux()

	payload := map[string]string{
		"type":   "url",
		"url":    "https://example.com/article",
		"action": "uinit_auto",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/share", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp ShareResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("response status = %q, want ok; message = %q", resp.Status, resp.Message)
	}

	// Verify the queue row was persisted (the share→queue guarantee).
	// After M4, resp.Prefiltered will be explicit false; for now verify via DB.
	var count int
	if resp.ID > 0 {
		if err := q.db.QueryRow("SELECT COUNT(*) FROM queue WHERE id=?", resp.ID).Scan(&count); err != nil {
			t.Fatalf("query queue row %d: %v", resp.ID, err)
		}
		if count != 1 {
			t.Errorf("queue row %d: count = %d, want 1", resp.ID, count)
		}
	} else {
		// Fallback: scan by URL (response.ID absent for older builds).
		if err := q.db.QueryRow(
			"SELECT COUNT(*) FROM queue WHERE url=?",
			"https://example.com/article",
		).Scan(&count); err != nil {
			t.Fatalf("query queue by url: %v", err)
		}
		if count == 0 {
			t.Error("expected a queue row for https://example.com/article, found none")
		}
	}
}

// ─── M3: handler.go:520 routing for YouTube URL with empty type ───────────────

// TestHandleShare_YouTubeURL_EmptyType verifies that a YouTube URL posted with
// req.Type="" routes to scoreYouTubeAsync (handler.go:520) rather than
// scoreAsync or being rejected by unsupportedPipeline. EPIC-005 M3.
func TestHandleShare_YouTubeURL_EmptyType(t *testing.T) {
	isolateEventsDir(t)
	installTestProfileDir(t, "eng")

	// Capture whether scoreYouTubeAsync was reached via the execYtdlp seam.
	// scoreYouTubeAsync calls execYtdlp as its first step; scoreAsync never does.
	var scoreYTCalled bool
	prevYtdlp := execYtdlp
	execYtdlp = func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		scoreYTCalled = true
		return "", ytVideoMeta{}, fmt.Errorf("stub: no subtitles — stop here")
	}
	t.Cleanup(func() { execYtdlp = prevYtdlp })

	// Disable audio fallback so the goroutine terminates quickly after yt-dlp.
	prevFallback := ytFallbackToAudio
	ytFallbackToAudio = false
	t.Cleanup(func() { ytFallbackToAudio = prevFallback })

	cfg := builtinConfig()
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, cfg, false)
	q := newTestQueue(t)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)
	mux := srv.Mux()

	payload := map[string]string{
		"type":   "",  // Missing type — the invariant under test at handler.go:520.
		"url":    "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"action": "uinit_auto",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/share", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", w.Code, w.Body.String())
	}

	// Give the goroutine time to call execYtdlp before we assert.
	time.Sleep(200 * time.Millisecond)

	if !scoreYTCalled {
		t.Error("execYtdlp not called — YouTube URL with type=\"\" did not route to scoreYouTubeAsync (handler.go:520)")
	}
}
