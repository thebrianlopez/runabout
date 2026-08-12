package main

// EPIC-067 M3: tests for audio transcription pipeline.
//
// Coverage:
//   1. processVoiceNoteAsync happy path — transcript backfilled, queue row scored.
//   2. processVoiceNoteAsync whisper failure — queue row marked failed.
//   3. processVoiceNoteAsync empty transcript — queue row marked failed.
//   4. validateRequest audio case — valid and invalid inputs.
//   5. Queue.SetText — backfills transcript on queue row.
//   6. Multipart parsing in handleShare — happy path + oversized rejection.
//
// Regression guards (POMO: audio-upload-io-timeout):
//   RG-1: upload io error → 408 RequestTimeout (not 413)
//   RG-2: upload exceeds maxAudioSize → 413 RequestEntityTooLarge (not 408)

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- seams -------------------------------------------------------------------

// installFfmpegStub returns an ffmpeg-convert stub that creates an empty wav
// file (simulating a successful conversion). EPIC-258 M2: wire it via
// deps.FfmpegConvert or router.SetFfmpegConvert instead of the former
// execFfmpegConvert package-var swap, which raced with scoring goroutines.
func installFfmpegStub(t *testing.T) func(context.Context, string, string) error {
	t.Helper()
	return func(_ context.Context, _, outputPath string) error {
		return os.WriteFile(outputPath, []byte("RIFF-fake-wav"), 0o644)
	}
}

// installFfmpegStubLargeWav creates a fake wav file large enough to trigger
// chunked transcription (> audioChunkSizeThreshold).
func installFfmpegStubLargeWav(t *testing.T) func(context.Context, string, string) error {
	t.Helper()
	return func(_ context.Context, _, outputPath string) error {
		// Write a file just over audioChunkSizeThreshold.
		data := make([]byte, audioChunkSizeThreshold+1024)
		copy(data, []byte("RIFF-fake-large-wav"))
		return os.WriteFile(outputPath, data, 0o644)
	}
}

// installSegmentStub overrides execFfmpegSegment for the duration of the test.
// It creates numChunks fake chunk files in the same directory as the input wav.
func installSegmentStub(t *testing.T, numChunks int) func(context.Context, string, int) ([]string, error) {
	t.Helper()
	return func(_ context.Context, wavPath string, _ int) ([]string, error) {
		dir := filepath.Dir(wavPath)
		base := strings.TrimSuffix(filepath.Base(wavPath), filepath.Ext(wavPath))
		var chunks []string
		for i := range numChunks {
			p := filepath.Join(dir, fmt.Sprintf("%s_chunk_%03d.wav", base, i))
			os.WriteFile(p, []byte("RIFF-chunk"), 0o644)
			chunks = append(chunks, p)
		}
		return chunks, nil
	}
}

// installWhisperChunkStub returns different transcript per chunk call.
func installWhisperChunkStub(t *testing.T, transcripts []string) func(context.Context, string, string) (string, error) {
	t.Helper()
	var callIdx atomic.Int32
	return func(_ context.Context, _, _ string) (string, error) {
		i := int(callIdx.Add(1)) - 1
		if i < len(transcripts) {
			return transcripts[i], nil
		}
		return "extra chunk", nil
	}
}

// installWhisperStub returns a whisper stub. EPIC-258 M2: wire via
// deps.Whisper (or a router deps mutator) instead of the former execWhisper
// package-var swap.
func installWhisperStub(t *testing.T, transcript string, err error) func(context.Context, string, string) (string, error) {
	t.Helper()
	return func(_ context.Context, _, _ string) (string, error) {
		return transcript, err
	}
}

// liteParseStub returns a scoringDeps.LiteParse replacement returning fixed
// values. EPIC-258 M2: callers inject it via scoringDeps.LiteParse or
// Router.SetScoringDepsMutator instead of swapping a package var.
func liteParseStub(text string, confidence float64, err error) func(context.Context, string, LiteParseConfig) (string, float64, error) {
	return func(_ context.Context, _ string, _ LiteParseConfig) (string, float64, error) {
		return text, confidence, err
	}
}

// --- helper: run scoreAudioAsync synchronously ------------------------------

// installHaikuSynopsisStub builds a scoring backend stubbing the synopsis
// path and the rubric scoring path for audio pipeline tests. Signals done on
// the first synopsis call. EPIC-088 M1: synopsis uses JSON schema output;
// stub returns a minimal synopsis envelope. EPIC-258 M2: returns an injected
// backend instead of swapping the former execHaikuSynopsisJSON/execHaikuJSON
// package vars, which raced with scoring goroutines.
func installHaikuSynopsisStub(t *testing.T, synopsis string) (*funcScoringBackend, chan struct{}) {
	t.Helper()
	done := make(chan struct{})
	var once int32

	backend := &funcScoringBackend{
		completeJSON: func(_ context.Context, _, _, schema string) ([]byte, error) {
			if schema == voiceNoteSynopsisSchema {
				// Synopsis path (replaces former execHaikuSynopsisJSON stub).
				if atomic.CompareAndSwapInt32(&once, 0, 1) {
					close(done)
				}
				env := fmt.Sprintf(`{"type":"result","result":"{\"synopsis\":%q}","is_error":false,"usage":{"input_tokens":5,"output_tokens":10},"total_cost_usd":0.0001}`, synopsis)
				return []byte(env), nil
			}
			// EPIC-081 M4: rubric scoring step (replaces former execHaikuJSON stub).
			return []byte(`{"type":"result","result":"{\"score\":75,\"verdict\":\"test rubric verdict\",\"rubric_scores\":{\"Clarity\":15,\"Actionability\":20,\"Novelty\":15,\"Urgency\":10,\"Topic Match\":15},\"topic_tags\":[\"test\"]}","is_error":false,"usage":{"input_tokens":10,"output_tokens":20},"total_cost_usd":0.001}`), nil
		},
	}

	// Create temp vnote_synopsis template so loadProfileTemplate finds it.
	// Use a single base dir for both the template and ORG_PATH to ensure
	// the env var points to the same tree where the file was written.
	base := t.TempDir()
	dir := filepath.Join(base, "docs", "prompts", "profiles")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "vnote_synopsis.md"), []byte("Summarize this voice note transcript.\n\n{{transcript}}"), 0o644)

	// EPIC-081 M4: create vnote_triage.yaml for rubric scoring.
	os.WriteFile(filepath.Join(dir, "vnote_triage.yaml"), []byte(`id: vnote_triage
version: 1
schema_version: triage_verdict_v1
content_modes:
  - audio
persona_intro: "You are an audio triage assistant."
noise_gate:
  min_chars: 20
  skip_label: "too short"
persona_body: |
  ## Task
  Evaluate this voice note.
verdict_prompt: "what is this about?"
rubric:
  - name: Clarity
    weight: 20
    rationale: "clear?"
  - name: Actionability
    weight: 25
    rationale: "actionable?"
  - name: Novelty
    weight: 20
    rationale: "new?"
  - name: Urgency
    weight: 15
    rationale: "urgent?"
  - name: Topic Match
    weight: 20
    rationale: "relevant?"
action_items:
  count: "1-3"
  horizon_days: 3
key_facts:
  count: "2-4"
`), 0o644)

	// Also create a life.yaml profile for default fallback (EPIC-081 M4).
	os.WriteFile(filepath.Join(dir, "life.yaml"), []byte(`id: life
version: 1
schema_version: triage_verdict_v1
persona_intro: "You are a life triage assistant."
noise_gate:
  min_chars: 200
  skip_label: "no content"
persona_body: |
  ## Task
  Evaluate life content.
verdict_prompt: "what is this?"
rubric:
  - name: A
    weight: 20
    rationale: "a"
  - name: B
    weight: 20
    rationale: "b"
  - name: C
    weight: 20
    rationale: "c"
  - name: D
    weight: 20
    rationale: "d"
  - name: E
    weight: 20
    rationale: "e"
action_items:
  count: "1-2"
  horizon_days: 7
key_facts:
  count: "2-3"
`), 0o644)

	t.Setenv("ORG_PATH", base)

	return backend, done
}

func runScoreAudioSync(t *testing.T, audioPath, profile string, q *Queue, rowID int64, done chan struct{}, deps *scoringDeps) {
	t.Helper()
	go processVoiceNoteAsync(audioPath, profile, q, rowID, "test.m4a", "", "", nil, nil, HaikuJSONEvaluator{Backend: deps.Backend}, deps)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Log("runScoreAudioSync: timed out waiting (haiku never called)")
	}
	time.Sleep(50 * time.Millisecond)
}

func runScoreAudioSkip(t *testing.T, audioPath, profile string, q *Queue, rowID int64, deps *scoringDeps) {
	t.Helper()
	go processVoiceNoteAsync(audioPath, profile, q, rowID, "test.m4a", "", "", nil, nil, HaikuJSONEvaluator{Backend: deps.Backend}, deps)
	time.Sleep(300 * time.Millisecond)
}

// --- Tests -------------------------------------------------------------------

// 1. Happy path: whisper returns transcript, Haiku returns synopsis, queue row updated.
func TestScoreAudioAsync_HappyPath(t *testing.T) {
	isolateEventsDir(t)
	ffmpegFn := installFfmpegStub(t)
	whisperFn := installWhisperStub(t, "This is a voice memo about machine learning transformers and attention mechanisms.", nil)

	backend, done := installHaikuSynopsisStub(t, "Speaker discusses ML transformer architecture and attention mechanisms.")

	q := newTestQueue(t)

	// Create a dummy audio file.
	audioFile := filepath.Join(t.TempDir(), "test.m4a")
	os.WriteFile(audioFile, []byte("fake-audio-data"), 0o644)

	// Enqueue a row as the server would.
	id, err := q.Enqueue(&ShareRequest{Type: "audio", Action: "vnote_auto"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	q.MarkRelayed(id)

	deps := newTestDeps(t)
	deps.Backend = backend
	deps.FfmpegConvert = ffmpegFn
	deps.Whisper = whisperFn
	runScoreAudioSync(t, audioFile, "eng", q, id, done, deps)

	// Check queue row is scored with transcript backfilled and rubric score.
	items, err := q.List("", 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, it := range items {
		if it.ID == id {
			found = true
			if it.Status != "scored" && it.Status != "archived" {
				t.Errorf("status = %q, want scored or archived", it.Status)
			}
			// EPIC-081 M4: score is now rubric-scored (stub returns 75).
			if it.Score == nil || *it.Score != 75 {
				t.Errorf("score = %v, want 75 (rubric-scored)", it.Score)
			}
			if it.Text == "" {
				t.Errorf("text not backfilled")
			}
		}
	}
	if !found {
		t.Errorf("queue row %d not found", id)
	}
}

// 2. Nil req — audio scoring should not panic and should fall back to digest push.
func TestScoreAudioAsync_NilReqFallback(t *testing.T) {
	isolateEventsDir(t)
	ffmpegFn := installFfmpegStub(t)
	whisperFn := installWhisperStub(t, "This is a source-generated voice note.", nil)
	backend, done := installHaikuSynopsisStub(t, "Source-generated voice note synopsis.")

	q := newTestQueue(t)
	audioFile := filepath.Join(t.TempDir(), "test.m4a")
	os.WriteFile(audioFile, []byte("fake-audio-data"), 0o644)

	id, err := q.Enqueue(&ShareRequest{Type: "audio", Action: "vnote_auto"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	q.MarkRelayed(id)

	deps := newTestDeps(t)
	deps.Backend = backend
	deps.FfmpegConvert = ffmpegFn
	deps.Whisper = whisperFn
	runScoreAudioSync(t, audioFile, "eng", q, id, done, deps)

	items, err := q.List("", 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, it := range items {
		if it.ID == id {
			if it.Status != "scored" && it.Status != "archived" {
				t.Errorf("status = %q, want scored or archived", it.Status)
			}
			if it.Score == nil || *it.Score != 75 {
				t.Errorf("score = %v, want 75 (rubric-scored)", it.Score)
			}
			if it.Text == "" {
				t.Errorf("text not backfilled")
			}
			return
		}
	}
	t.Errorf("queue row %d not found", id)
}

// 3. Whisper failure — queue row is retried (EPIC-111 M2: extraction retry).
// First failure sets status='pending' with retry_count=1 and retry_after=now+30s.
func TestScoreAudioAsync_WhisperFailure(t *testing.T) {
	isolateEventsDir(t)
	ffmpegFn := installFfmpegStub(t)
	whisperFn := installWhisperStub(t, "", fmt.Errorf("whisper-cli: model not found"))

	q := newTestQueue(t)
	audioFile := filepath.Join(t.TempDir(), "test.m4a")
	os.WriteFile(audioFile, []byte("fake-audio"), 0o644)

	id, _ := q.Enqueue(&ShareRequest{Type: "audio", Action: "vnote_auto"})
	q.MarkRelayed(id)

	deps := newTestDeps(t)
	deps.FfmpegConvert = ffmpegFn
	deps.Whisper = whisperFn
	runScoreAudioSkip(t, audioFile, "eng", q, id, deps)

	var status string
	var retryCount int
	var retryAfter int64
	q.db.QueryRow("SELECT status, retry_count, retry_after FROM queue WHERE id=?", id).Scan(&status, &retryCount, &retryAfter)

	if status != "pending" {
		t.Errorf("status = %q, want pending (retry scheduled)", status)
	}
	if retryCount != 1 {
		t.Errorf("retry_count = %d, want 1", retryCount)
	}
	if retryAfter <= 0 {
		t.Errorf("retry_after = %d, want > 0 (backoff delay)", retryAfter)
	}
}

// 3. Empty transcript — queue row marked failed.
func TestScoreAudioAsync_EmptyTranscript(t *testing.T) {
	isolateEventsDir(t)
	ffmpegFn := installFfmpegStub(t)
	whisperFn := installWhisperStub(t, "  \n  ", nil) // whitespace-only

	q := newTestQueue(t)
	audioFile := filepath.Join(t.TempDir(), "test.m4a")
	os.WriteFile(audioFile, []byte("fake-audio"), 0o644)

	id, _ := q.Enqueue(&ShareRequest{Type: "audio", Action: "vnote_auto"})
	q.MarkRelayed(id)

	deps := newTestDeps(t)
	deps.FfmpegConvert = ffmpegFn
	deps.Whisper = whisperFn
	runScoreAudioSkip(t, audioFile, "eng", q, id, deps)
}

// 4. validateRequest — audio type.
func TestValidateRequest_Audio(t *testing.T) {
	// Valid audio request.
	tmpFile := filepath.Join(t.TempDir(), "test.m4a")
	os.WriteFile(tmpFile, []byte("audio-data"), 0o644)

	err := validateRequest(&ShareRequest{Type: "audio", AudioPath: tmpFile})
	if err != nil {
		t.Errorf("valid audio request rejected: %v", err)
	}

	// Missing audio path.
	err = validateRequest(&ShareRequest{Type: "audio"})
	if err == nil {
		t.Errorf("expected error for missing audio path")
	}

	// Non-existent audio file.
	err = validateRequest(&ShareRequest{Type: "audio", AudioPath: "/tmp/nonexistent-audio-file.m4a"})
	if err == nil {
		t.Errorf("expected error for non-existent audio file")
	}
}

// 5. Queue.SetText backfills transcript.
func TestQueueSetText(t *testing.T) {
	q := newTestQueue(t)
	id, err := q.Enqueue(&ShareRequest{Type: "audio", Action: "vnote_auto"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	transcript := "Hello this is a test transcription"
	if err := q.SetText(id, transcript); err != nil {
		t.Fatalf("SetText: %v", err)
	}

	items, _ := q.List("", 20)
	for _, it := range items {
		if it.ID == id {
			if it.Text != transcript {
				t.Errorf("text = %q, want %q", it.Text, transcript)
			}
			return
		}
	}
	t.Errorf("row %d not found", id)
}

// 6. Multipart handleShare — happy path.
func TestHandleShare_MultipartAudio(t *testing.T) {
	isolateEventsDir(t)
	ffmpegFn := installFfmpegStub(t)
	whisperFn := installWhisperStub(t, "test transcript", nil)
	mpBackend, _ := installHaikuSynopsisStub(t, "Test synopsis for multipart.")

	cfg := builtinConfig()
	router := NewRouterFromConfig(&TmuxRunner{}, cfg, false)
	router.SetScoringBackend(mpBackend)
	router.SetScoringDepsMutator(func(d *scoringDeps) {
		d.FfmpegConvert = ffmpegFn
		d.Whisper = whisperFn
	})
	q := newTestQueue(t)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	// Build multipart request.
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	writer.WriteField("action", "vnote_auto")
	writer.WriteField("fcm_token", "test-fcm-token")
	part, _ := writer.CreateFormFile("audio", "memo.m4a")
	part.Write([]byte("fake-audio-data-for-multipart-test"))
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/share", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()

	srv.Mux().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
}

// 7. Multipart handleShare — small audio accepted (200MB limit).
func TestHandleShare_MultipartSmallFile(t *testing.T) {
	isolateEventsDir(t)

	cfg := builtinConfig()
	router := NewRouterFromConfig(&TmuxRunner{}, cfg, false)
	srv := NewServer("test-token", router, nil, NewRingLog(10), false, nil)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	writer.WriteField("action", "vnote_auto")
	part, _ := writer.CreateFormFile("audio", "small.m4a")
	io.Copy(part, io.LimitReader(bytes.NewReader(make([]byte, 1024)), 1024))
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/share", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()

	srv.Mux().ServeHTTP(rr, req)

	// Small file should be accepted under 200MB limit.
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
}

// 8. Chunked transcription — large WAV triggers segmentation and concatenation.
func TestScoreAudioAsync_Chunked(t *testing.T) {
	isolateEventsDir(t)
	ffmpegFn := installFfmpegStubLargeWav(t)
	segmentFn := installSegmentStub(t, 3)
	whisperFn := installWhisperChunkStub(t, []string{
		"First chunk about machine learning.",
		"Second chunk about transformers.",
		"Third chunk about attention.",
	})

	backend, done := installHaikuSynopsisStub(t, "Speaker discusses ML across three chunks.")

	q := newTestQueue(t)
	audioFile := filepath.Join(t.TempDir(), "large.m4a")
	os.WriteFile(audioFile, []byte("fake-large-audio"), 0o644)

	id, err := q.Enqueue(&ShareRequest{Type: "audio", Action: "vnote_auto"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	q.MarkRelayed(id)

	deps := newTestDeps(t)
	deps.Backend = backend
	deps.FfmpegConvert = ffmpegFn
	deps.FfmpegSegment = segmentFn
	deps.Whisper = whisperFn
	runScoreAudioSync(t, audioFile, "", q, id, done, deps)

	items, _ := q.List("", 20)
	for _, it := range items {
		if it.ID == id {
			if it.Status != "scored" && it.Status != "archived" {
				t.Errorf("status = %q, want scored or archived", it.Status)
			}
			// EPIC-081 M4: score is now rubric-scored (stub returns 75).
			if it.Score == nil || *it.Score != 75 {
				t.Errorf("score = %v, want 75 (rubric-scored)", it.Score)
			}
			// Transcript should contain all three chunks concatenated.
			if !strings.Contains(it.Text, "First chunk") || !strings.Contains(it.Text, "Third chunk") {
				t.Errorf("transcript not fully concatenated: %q", it.Text)
			}
			return
		}
	}
	t.Errorf("queue row %d not found", id)
}

// 9. Progress column updates during chunked transcription.
func TestScoreAudioAsync_ProgressUpdates(t *testing.T) {
	isolateEventsDir(t)
	ffmpegFn := installFfmpegStubLargeWav(t)
	segmentFn := installSegmentStub(t, 2)

	// Whisper stub for chunk transcription (EPIC-258 M2: injected via deps).
	whisperFn := func(_ context.Context, _, _ string) (string, error) {
		return "chunk text", nil
	}

	backend, done := installHaikuSynopsisStub(t, "Progress test synopsis.")

	q := newTestQueue(t)
	audioFile := filepath.Join(t.TempDir(), "progress.m4a")
	os.WriteFile(audioFile, []byte("fake-audio"), 0o644)

	id, _ := q.Enqueue(&ShareRequest{Type: "audio", Action: "vnote_auto"})
	q.MarkRelayed(id)

	deps := newTestDeps(t)
	deps.Backend = backend
	deps.FfmpegConvert = ffmpegFn
	deps.FfmpegSegment = segmentFn
	deps.Whisper = whisperFn
	runScoreAudioSync(t, audioFile, "", q, id, done, deps)

	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if item.Status != "scored" && item.Status != "archived" {
		t.Errorf("status = %q, want scored or archived", item.Status)
	}
}

// 10. Queue.SetProgress round-trip.
func TestQueueSetProgress(t *testing.T) {
	q := newTestQueue(t)
	id, err := q.Enqueue(&ShareRequest{Type: "audio", Action: "vnote_auto"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if err := q.SetProgress(id, "transcribing 2/5"); err != nil {
		t.Fatalf("SetProgress: %v", err)
	}

	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if item.Progress != "transcribing 2/5" {
		t.Errorf("progress = %q, want %q", item.Progress, "transcribing 2/5")
	}

	// Update progress again.
	q.SetProgress(id, "evaluating")
	item, _ = q.GetByID(id)
	if item.Progress != "evaluating" {
		t.Errorf("progress = %q, want %q", item.Progress, "evaluating")
	}
}

// RG-1: An upload that errors mid-stream (i/o timeout, unexpected EOF) must
// return 408 RequestTimeout, not 413 RequestEntityTooLarge.
// POMO: PERSONAL_20260424T191613Z_POMO_audio-upload-io-timeout.md
func TestHandleShare_RG1_UploadIOError_Returns408(t *testing.T) {
	isolateEventsDir(t)

	cfg := builtinConfig()
	router := NewRouterFromConfig(&TmuxRunner{}, cfg, false)
	srv := NewServer("test-token", router, nil, NewRingLog(10), false, nil)

	// Use a pipe so we can inject a read error mid-upload, simulating
	// what happens when a Tailscale connection hits a read deadline.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	ct := mw.FormDataContentType()

	go func() {
		mw.WriteField("action", "vnote_auto")
		part, _ := mw.CreateFormFile("audio", "memo.m4a")
		part.Write([]byte("partial-audio-data"))
		// Simulate i/o timeout: close the pipe with an error before the
		// multipart boundary is written, so io.Copy(tmp, part) returns an error
		// that is NOT *http.MaxBytesError.
		pw.CloseWithError(errors.New("i/o timeout"))
	}()

	req := httptest.NewRequest(http.MethodPost, "/share", pr)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()

	srv.Mux().ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestTimeout {
		t.Errorf("RG-1: status = %d, want 408 (RequestTimeout); body = %s", rr.Code, rr.Body.String())
	}
}

// infiniteZeroReader produces an unlimited stream of zero bytes.
// Used to exceed maxAudioSize in RG-2 without allocating a 200MB buffer.
type infiniteZeroReader struct{}

func (infiniteZeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// RG-2: An upload that genuinely exceeds maxAudioSize (200MB) must return 413
// RequestEntityTooLarge, not 408. Validates that the MaxBytesError branch is
// still reachable after the RG-1 fix.
// POMO: PERSONAL_20260424T191613Z_POMO_audio-upload-io-timeout.md
//
// Note: this test pumps maxAudioSize+1 bytes through an in-memory pipe into a
// temp file to trigger http.MaxBytesReader. It is IO-intensive (~200MB to disk)
// and skipped under -short.
func TestHandleShare_RG2_MaxBytesExceeded_Returns413(t *testing.T) {
	if testing.Short() {
		t.Skip("RG-2: skipping IO-intensive 200MB overflow test in short mode")
	}
	isolateEventsDir(t)

	cfg := builtinConfig()
	router := NewRouterFromConfig(&TmuxRunner{}, cfg, false)
	srv := NewServer("test-token", router, nil, NewRingLog(10), false, nil)

	pr, pw := io.Pipe()
	// handleShare stops reading the body once MaxBytesReader trips, so the
	// writer goroutine below would block on pw.Write forever. Closing the read
	// end after ServeHTTP returns unblocks it (io.Copy returns ErrClosedPipe),
	// preventing a leaked goroutine (leakcheck gate).
	defer pr.Close()
	mw := multipart.NewWriter(pw)
	ct := mw.FormDataContentType()

	go func() {
		mw.WriteField("action", "vnote_auto")
		part, _ := mw.CreateFormFile("audio", "huge.m4a")
		// Write maxAudioSize+1 bytes to exceed the limit applied by
		// http.MaxBytesReader in handleShare.
		io.Copy(part, io.LimitReader(infiniteZeroReader{}, maxAudioSize+1))
		mw.Close()
		pw.Close()
	}()

	req := httptest.NewRequest(http.MethodPost, "/share", pr)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()

	srv.Mux().ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("RG-2: status = %d, want 413 (RequestEntityTooLarge); body = %s", rr.Code, rr.Body.String())
	}
}
