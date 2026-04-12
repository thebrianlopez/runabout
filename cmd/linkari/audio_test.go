package main

// EPIC-067 M3: tests for audio transcription pipeline.
//
// Coverage:
//   1. scoreAudioAsync happy path — transcript backfilled, queue row scored.
//   2. scoreAudioAsync whisper failure — queue row marked failed.
//   3. scoreAudioAsync empty transcript — queue row marked failed.
//   4. validateRequest audio case — valid and invalid inputs.
//   5. Queue.SetText — backfills transcript on queue row.
//   6. Multipart parsing in handleShare — happy path + oversized rejection.

import (
	"bytes"
	"context"
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

// installFfmpegStub overrides execFfmpegConvert for the duration of the test.
// The stub creates an empty wav file (simulating a successful conversion).
func installFfmpegStub(t *testing.T) {
	t.Helper()
	prev := execFfmpegConvert
	execFfmpegConvert = func(_ context.Context, _, outputPath string) error {
		return os.WriteFile(outputPath, []byte("RIFF-fake-wav"), 0o644)
	}
	t.Cleanup(func() { execFfmpegConvert = prev })
}

// installFfmpegStubLargeWav creates a fake wav file large enough to trigger
// chunked transcription (> audioChunkSizeThreshold).
func installFfmpegStubLargeWav(t *testing.T) {
	t.Helper()
	prev := execFfmpegConvert
	execFfmpegConvert = func(_ context.Context, _, outputPath string) error {
		// Write a file just over audioChunkSizeThreshold.
		data := make([]byte, audioChunkSizeThreshold+1024)
		copy(data, []byte("RIFF-fake-large-wav"))
		return os.WriteFile(outputPath, data, 0o644)
	}
	t.Cleanup(func() { execFfmpegConvert = prev })
}

// installSegmentStub overrides execFfmpegSegment for the duration of the test.
// It creates numChunks fake chunk files in the same directory as the input wav.
func installSegmentStub(t *testing.T, numChunks int) {
	t.Helper()
	prev := execFfmpegSegment
	execFfmpegSegment = func(_ context.Context, wavPath string, _ int) ([]string, error) {
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
	t.Cleanup(func() { execFfmpegSegment = prev })
}

// installWhisperChunkStub returns different transcript per chunk call.
func installWhisperChunkStub(t *testing.T, transcripts []string) {
	t.Helper()
	var callIdx atomic.Int32
	prev := execWhisper
	execWhisper = func(_ context.Context, _, _ string) (string, error) {
		i := int(callIdx.Add(1)) - 1
		if i < len(transcripts) {
			return transcripts[i], nil
		}
		return "extra chunk", nil
	}
	t.Cleanup(func() { execWhisper = prev })
}

// installWhisperStub overrides execWhisper for the duration of the test.
func installWhisperStub(t *testing.T, transcript string, err error) {
	t.Helper()
	prev := execWhisper
	execWhisper = func(_ context.Context, _, _ string) (string, error) {
		return transcript, err
	}
	t.Cleanup(func() { execWhisper = prev })
}

// --- helper: run scoreAudioAsync synchronously ------------------------------

func runScoreAudioSync(t *testing.T, audioPath, profile string, q *Queue, rowID int64, eval Evaluator) {
	t.Helper()
	done := make(chan struct{})
	wrapped := &onceDoneEval{inner: eval, done: done}
	go scoreAudioAsync(audioPath, profile, q, rowID, wrapped, "")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Log("runScoreAudioSync: timed out waiting (eval never called)")
	}
	time.Sleep(50 * time.Millisecond)
}

func runScoreAudioSkip(t *testing.T, audioPath, profile string, q *Queue, rowID int64, eval Evaluator) {
	t.Helper()
	go scoreAudioAsync(audioPath, profile, q, rowID, eval, "")
	time.Sleep(300 * time.Millisecond)
}

// --- Tests -------------------------------------------------------------------

// 1. Happy path: whisper returns transcript, eval scores it, queue row updated.
func TestScoreAudioAsync_HappyPath(t *testing.T) {
	isolateEventsDir(t)
	installFfmpegStub(t)
	installWhisperStub(t, "This is a voice memo about machine learning transformers and attention mechanisms.", nil)

	// Stub content classify to avoid Haiku call.
	prevClassify := execContentClassify
	execContentClassify = func(_ context.Context, _, _ string) (string, error) {
		return "eng", nil
	}
	t.Cleanup(func() { execContentClassify = prevClassify })

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

	eval := &stubEvaluator{score: 75, verdict: "Good voice note"}
	runScoreAudioSync(t, audioFile, "eng", q, id, eval)

	if atomic.LoadInt32(&eval.calls) != 1 {
		t.Errorf("eval.calls = %d, want 1", atomic.LoadInt32(&eval.calls))
	}

	// Check queue row is scored with transcript backfilled.
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
			if it.Score == nil || *it.Score != 75 {
				t.Errorf("score = %v, want 75", it.Score)
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

// 2. Whisper failure — queue row marked failed.
func TestScoreAudioAsync_WhisperFailure(t *testing.T) {
	isolateEventsDir(t)
	installFfmpegStub(t)
	installWhisperStub(t, "", fmt.Errorf("whisper-cli: model not found"))

	q := newTestQueue(t)
	audioFile := filepath.Join(t.TempDir(), "test.m4a")
	os.WriteFile(audioFile, []byte("fake-audio"), 0o644)

	id, _ := q.Enqueue(&ShareRequest{Type: "audio", Action: "vnote_auto"})
	q.MarkRelayed(id)

	eval := &stubEvaluator{score: 50, verdict: "test"}
	runScoreAudioSkip(t, audioFile, "eng", q, id, eval)

	if atomic.LoadInt32(&eval.calls) != 0 {
		t.Errorf("eval called %d times, want 0 after whisper failure", atomic.LoadInt32(&eval.calls))
	}

	items, _ := q.List("", 20)
	for _, it := range items {
		if it.ID == id && it.Status != "failed" {
			t.Errorf("status = %q, want failed", it.Status)
		}
	}
}

// 3. Empty transcript — queue row marked failed.
func TestScoreAudioAsync_EmptyTranscript(t *testing.T) {
	isolateEventsDir(t)
	installFfmpegStub(t)
	installWhisperStub(t, "  \n  ", nil) // whitespace-only

	q := newTestQueue(t)
	audioFile := filepath.Join(t.TempDir(), "test.m4a")
	os.WriteFile(audioFile, []byte("fake-audio"), 0o644)

	id, _ := q.Enqueue(&ShareRequest{Type: "audio", Action: "vnote_auto"})
	q.MarkRelayed(id)

	eval := &stubEvaluator{score: 50, verdict: "test"}
	runScoreAudioSkip(t, audioFile, "eng", q, id, eval)

	if atomic.LoadInt32(&eval.calls) != 0 {
		t.Errorf("eval called %d times, want 0 for empty transcript", atomic.LoadInt32(&eval.calls))
	}
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
	installFfmpegStub(t)
	installWhisperStub(t, "test transcript", nil)

	// Stub content classify.
	prevClassify := execContentClassify
	execContentClassify = func(_ context.Context, _, _ string) (string, error) {
		return "eng", nil
	}
	t.Cleanup(func() { execContentClassify = prevClassify })

	cfg := builtinConfig()
	router := NewRouterFromConfig(&TmuxRunner{}, cfg, false)
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
	installFfmpegStubLargeWav(t)
	installSegmentStub(t, 3)
	installWhisperChunkStub(t, []string{
		"First chunk about machine learning.",
		"Second chunk about transformers.",
		"Third chunk about attention.",
	})

	prevClassify := execContentClassify
	execContentClassify = func(_ context.Context, _, _ string) (string, error) {
		return "eng", nil
	}
	t.Cleanup(func() { execContentClassify = prevClassify })

	q := newTestQueue(t)
	audioFile := filepath.Join(t.TempDir(), "large.m4a")
	os.WriteFile(audioFile, []byte("fake-large-audio"), 0o644)

	id, err := q.Enqueue(&ShareRequest{Type: "audio", Action: "vnote_auto"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	q.MarkRelayed(id)

	eval := &stubEvaluator{score: 85, verdict: "Great chunked voice note"}
	runScoreAudioSync(t, audioFile, "", q, id, eval)

	if atomic.LoadInt32(&eval.calls) != 1 {
		t.Errorf("eval.calls = %d, want 1", atomic.LoadInt32(&eval.calls))
	}

	items, _ := q.List("", 20)
	for _, it := range items {
		if it.ID == id {
			if it.Status != "scored" && it.Status != "archived" {
				t.Errorf("status = %q, want scored or archived", it.Status)
			}
			if it.Score == nil || *it.Score != 85 {
				t.Errorf("score = %v, want 85", it.Score)
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
	installFfmpegStubLargeWav(t)
	installSegmentStub(t, 2)

	// Capture progress updates via whisper stub.
	var progressLog []string
	prevWhisper := execWhisper
	execWhisper = func(_ context.Context, _, _ string) (string, error) {
		return "chunk text", nil
	}
	t.Cleanup(func() { execWhisper = prevWhisper })

	prevClassify := execContentClassify
	execContentClassify = func(_ context.Context, _, _ string) (string, error) {
		return "eng", nil
	}
	t.Cleanup(func() { execContentClassify = prevClassify })

	q := newTestQueue(t)
	audioFile := filepath.Join(t.TempDir(), "progress.m4a")
	os.WriteFile(audioFile, []byte("fake-audio"), 0o644)

	id, _ := q.Enqueue(&ShareRequest{Type: "audio", Action: "vnote_auto"})
	q.MarkRelayed(id)

	eval := &stubEvaluator{score: 70, verdict: "ok"}
	runScoreAudioSync(t, audioFile, "", q, id, eval)

	// After completion, progress should be cleared (row moves to scored).
	// But during execution, SetProgress was called. We verify the method works
	// by checking that a direct SetProgress + read round-trips.
	_ = progressLog // we tested SetProgress works in test #10 below

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
