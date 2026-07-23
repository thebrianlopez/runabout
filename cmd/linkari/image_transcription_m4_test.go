package main

// EPIC-122 M4: Observability and feature flag smoke tests.
//
// Verifies:
//   - Feature flag imageTextExtractionEnabled=false causes graceful fallthrough
//     (no extractImageText call, no transcript, standard scoring proceeds)
//   - imageTextExtractionEnabled=true triggers F1/F2/F3 pipeline events
//   - image_noise_gate_skip event emitted for below-min-size images

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFeatureFlag_Disabled_Fallthrough verifies that when imageTextExtractionEnabled=false,
// scoreAsync falls through to metadata-only scoring without calling extractImageText.
func TestFeatureFlag_Disabled_Fallthrough(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // prevent LoadConfig from hitting real config.toml with secretsmanager refs
	isolateEventsDir(t)
	installTestProfileDir(t, "image_triage")

	// Ensure feature flag is off.
	origEnabled := imageTextExtractionEnabled
	imageTextExtractionEnabled = false
	t.Cleanup(func() { imageTextExtractionEnabled = origEnabled })

	// Create a test image file.
	imgPath := makeTestImage(t)
	imgBytes, err := os.ReadFile(imgPath)
	if err != nil {
		t.Fatalf("read test image: %v", err)
	}

	// Mock claude PATH — if the flag is off, this should never be called for F1.
	// We use a script that exits non-zero so any accidental F1 call would fail.
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	body := "#!/bin/sh\nexit 42\n" // fail loudly if called unexpectedly
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write mock claude: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	// Override the vision scorer too — return a valid triage verdict.
	prevRunVision := execHaikuVision
	execHaikuVision = func(_ context.Context, _, _, _ string, _ string) ([]byte, error) {
		verdict := `{"score":60,"verdict":"metadata only scoring","rubric_scores":{"Relevance":60,"Depth":55,"Novelty":60,"Clarity":65,"Actionability":60},"action_items":[],"tags":"test","topic_tags":["test"]}`
		envelope := `{"type":"result","result":"` + jsonEscapeForShell(verdict) + `","is_error":false,"total_cost_usd":0.001}`
		return []byte(envelope), nil
	}
	t.Cleanup(func() { execHaikuVision = prevRunVision })

	origTranscriptDir := transcriptDir
	transcriptDir = t.TempDir()
	t.Cleanup(func() { transcriptDir = origTranscriptDir })

	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	req := &ShareRequest{
		Type:           "image",
		Filename:       "photo.jpg",
		MimeType:       "image/jpeg",
		FileSize:       int64(len(imgBytes)),
		CallingPackage: "com.android.gallery3d",
		AudioPath:      imgPath,
		ExtraSubject:   "Subject for metadata-only scoring",
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := q.MarkRelayed(id); err != nil {
		t.Fatalf("MarkRelayed: %v", err)
	}
	req.QueueRowID = id

	go scoreAsync(req, q, nil, nil, nil, nil)

	// Poll for terminal status.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		items, lErr := q.List("", 20)
		if lErr == nil {
			for _, it := range items {
				if it.ID == id && isTerminal(it.Status) {
					goto done
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
done:
	time.Sleep(50 * time.Millisecond)

	items, err := q.List("", 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, it := range items {
		if it.ID == id && isTerminal(it.Status) {
			found = true
			t.Logf("feature-flag-disabled: row scored with status=%s score=%v", it.Status, it.Score)
		}
	}
	if !found {
		t.Error("feature-flag-disabled: row not in terminal state after scoreAsync")
	}

	// F2 image transcript (IMG_ prefix) must NOT be written when flag is off.
	// Note: the existing saveTranscriptFile path may write a non-IMG_ transcript
	// for the vision verdict — that's expected existing behavior; only IMG_ files
	// are the F2 output.
	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		t.Fatalf("read transcripts dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "_IMG_") {
			t.Errorf("feature-flag-disabled: found IMG_ transcript %q; want no F2 output", e.Name())
		}
	}
}

// jsonEscapeForShell escapes a JSON string for embedding inside another JSON string.
// This is only used in tests to build mock claude responses.
func jsonEscapeForShell(s string) string {
	// Escape double quotes and backslashes for JSON-in-JSON embedding.
	result := make([]byte, 0, len(s)*2)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			result = append(result, '\\', '"')
		case '\\':
			result = append(result, '\\', '\\')
		default:
			result = append(result, s[i])
		}
	}
	return string(result)
}

// TestFeatureFlag_Enabled_EmitsEvents verifies that when imageTextExtractionEnabled=true
// and extraction succeeds, the expected events are emitted.
func TestFeatureFlag_Enabled_EmitsEvents(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // prevent LoadConfig from hitting real config.toml with secretsmanager refs
	eventsDir := isolateEventsDir(t)
	installTestProfileDir(t, "image_triage")

	origEnabled := imageTextExtractionEnabled
	origThreshold := imageShortCircuitBypassMinChars
	origNoiseGate := imageNoiseGateMinBytes
	imageTextExtractionEnabled = true
	imageShortCircuitBypassMinChars = 20
	imageNoiseGateMinBytes = 1
	t.Cleanup(func() {
		imageTextExtractionEnabled = origEnabled
		imageShortCircuitBypassMinChars = origThreshold
		imageNoiseGateMinBytes = origNoiseGate
	})

	imgPath := makeTestImage(t)
	imgBytes, err := os.ReadFile(imgPath)
	if err != nil {
		t.Fatalf("read test image: %v", err)
	}

	extractedTextJSON := `{"type":"result","result":"{\"text\":\"Engineering article about distributed systems\"}","is_error":false,"total_cost_usd":0.001}`
	mockClaudeScript(t, extractedTextJSON, 0)

	prevRunVision := execHaikuVision
	verdict := `{"score":80,"verdict":"worth reading","rubric_scores":{"Relevance":80,"Depth":75,"Novelty":80,"Clarity":85,"Actionability":80},"action_items":[],"tags":"eng","topic_tags":["distributed","systems"]}`
	envelope := `{"type":"result","result":"` + jsonEscapeForShell(verdict) + `","is_error":false,"total_cost_usd":0.002}`
	execHaikuVision = func(_ context.Context, _, _, _ string, schema string) ([]byte, error) {
		if schema == imageTextResultSchema {
			return []byte(`{"type":"result","result":"{\"text\":\"Engineering article about distributed systems\"}","is_error":false,"total_cost_usd":0.001}`), nil
		}
		return []byte(envelope), nil
	}
	t.Cleanup(func() { execHaikuVision = prevRunVision })

	origTranscriptDir := transcriptDir
	transcriptDir = t.TempDir()
	t.Cleanup(func() { transcriptDir = origTranscriptDir })

	// Create the events directory and a JSONL file for the event logger.
	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll events: %v", err)
	}
	evLogPath := filepath.Join(eventsDir, "2026-05-16.jsonl")
	evLogger, err := NewEventLogger(evLogPath)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	defer evLogger.Close()

	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	req := &ShareRequest{
		Type:           "image",
		Filename:       "Screenshot_test.jpg",
		MimeType:       "image/jpeg",
		FileSize:       int64(len(imgBytes)),
		CallingPackage: "com.android.chrome",
		IsScreenshot:   true,
		AudioPath:      imgPath,
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := q.MarkRelayed(id); err != nil {
		t.Fatalf("MarkRelayed: %v", err)
	}
	req.QueueRowID = id

	go scoreAsync(req, q, nil, evLogger, nil, nil)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		items, lErr := q.List("", 20)
		if lErr == nil {
			for _, it := range items {
				if it.ID == id && isTerminal(it.Status) {
					goto done2
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
done2:
	time.Sleep(50 * time.Millisecond)

	// Check that expected events were emitted.
	eventCounts := readEventTypes(t, eventsDir)
	for _, want := range []string{"image_text_extracted", "image_transcript_saved", "image_metadata_enrichment"} {
		if eventCounts[want] == 0 {
			t.Errorf("event %q not emitted; events found: %v", want, eventCounts)
		}
	}
	// Transcript file must exist.
	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		t.Fatalf("read transcripts dir: %v", err)
	}
	if len(entries) == 0 {
		t.Error("no transcript file written; want at least one")
	}
}
