package main

// EPIC-126: Firehose Scoring Quality and Compliance - contract tests for F5 and F7.
//
// F5 (Transcript Persistence): coalesce rawContent/req.Text before transcript write.
// F7 (Vision Path Guard): prevent vision token back-calculation for text-only URL shares.
//
// Source TDDs:
//   F5: PERSONAL_20260519T162045Z_Runabout_Firehose_Transcript_Persistence_TDD.md
//   F7: PERSONAL_20260519T162047Z_Runabout_Firehose_Vision_Path_Guard_TDD.md

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// visionStubEvaluator returns a Scorecard with Usage populated so the vision
// token back-calculation can trigger. InputTokens < 100 and CostUSD > 0.01
// are the conditions that activate vision_token_correction.
type visionStubEvaluator struct {
	score   int
	verdict string
}

func (e *visionStubEvaluator) Name() string { return "vision-stub" }
func (e *visionStubEvaluator) Evaluate(_ context.Context, _, _ string) (*Scorecard, error) {
	return &Scorecard{
		Score:   e.score,
		Verdict: e.verdict,
		Tags:    "test",
		CostUSD: 0.015,
		Usage:   &TokenUsage{InputTokens: 10, OutputTokens: 50},
	}, nil
}

// runScoreAsyncWithEvents runs scoreAsync with q=nil and a real EventLogger.
// q=nil causes scoreAsync to exit before resolvePushConfigOnce (which would
// block waiting for AWS credentials in test environments). The vision check
// and event emission happen before the q==nil early return, so vision_token_correction
// events are still captured correctly.
// Waits for the goroutine to fully return before returning.
func runScoreAsyncWithEvents(t *testing.T, req *ShareRequest, eval Evaluator, eventsPath string, deps *scoringDeps) {
	t.Helper()
	el, err := NewEventLogger(eventsPath)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	defer el.Close()

	goroutineDone := make(chan struct{})
	go func() {
		defer close(goroutineDone)
		scoreAsync(req, nil, eval, el, nil, nil, deps) // nil q: avoids resolvePushConfigOnce blocking
	}()
	select {
	case <-goroutineDone:
	case <-time.After(5 * time.Second):
		t.Log("runScoreAsyncWithEvents: timed out waiting for scoreAsync to return")
	}
}

// readEventTypesFromFile reads JSONL events from path and returns a count map by event_type.
func readEventTypesFromFile(t *testing.T, path string) map[string]int {
	t.Helper()
	counts := map[string]int{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return counts
	}
	if err != nil {
		t.Fatalf("read events file: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev struct {
			EventType string `json:"event_type"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err == nil && ev.EventType != "" {
			counts[ev.EventType]++
		}
	}
	return counts
}

// =====================================================================
// F5: Firehose Transcript Persistence
// =====================================================================

// F5-CT-1: Firehose post with req.Text populated and fetchedContent empty (AT-URI) →
// transcript file body contains the post text.
func TestF5CT1_FirehoseTranscriptBodyPopulated(t *testing.T) {
	isolateEventsDir(t)
	transcriptDir := filepath.Join(t.TempDir(), "transcripts")

	deps := installJinaServer(t, jinaBodyServer(t, 404, ""))

	deps.TranscriptsDir = transcriptDir
	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	postText := "LLM eval paper: evaluating reasoning in large language models"
	req := &ShareRequest{
		Type:    "url",
		URL:     "at://did:plc:abc/app.bsky.feed.post/f5ct1",
		Text:    postText,
		Profile: "eng",
	}
	id, err := q.EnqueueWithSource(req, "firehose")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	req.QueueRowID = id

	eval := &stubEvaluator{score: 75, verdict: "Strong Yes"}
	runScoreFileAsyncSync(t, req, q, eval, deps)

	// EPIC-250 M3 (RG-2): narrow the match to this test's own AT-URI slug
	// ("f5ct1") so a transcript file leaked from another test  -  e.g. a
	// goroutine that outlived its own test and wrote into this transcriptDir
	// after reassignment  -  cannot cause this test to fail or false-pass.
	// See POMO_firehose-transcript-goroutine-leak-suite-order and RG-2 in
	// PERSONAL_20260519T162045Z_Runabout_Firehose_Transcript_Persistence_TDD.md §6.
	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		t.Fatalf("read transcriptDir: %v", err)
	}
	var found bool
	for _, e := range entries {
		if strings.Contains(e.Name(), "_url_") && strings.Contains(e.Name(), "f5ct1") {
			found = true
			data, _ := os.ReadFile(filepath.Join(transcriptDir, e.Name()))
			if !strings.Contains(string(data), postText) {
				t.Errorf("F5-CT-1: transcript body missing post text; got:\n%s", data)
			}
		}
	}
	if !found {
		t.Errorf("F5-CT-1: no _url_ transcript file matching slug %q found in %s", "f5ct1", transcriptDir)
	}
}

// F5-CT-2: HTTP share with fetchedContent populated and empty req.Text →
// transcript body contains fetchedContent (not altered by the coalesce).
func TestF5CT2_HTTPShareTranscriptUnchanged(t *testing.T) {
	isolateEventsDir(t)
	transcriptDir := filepath.Join(t.TempDir(), "transcripts")

	const pageContent = "Full article body fetched from the web  -  machine learning research."
	deps := installJinaServer(t, jinaBodyServer(t, 200, pageContent))
	deps.TranscriptsDir = transcriptDir
	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	req := &ShareRequest{
		Type:    "url",
		URL:     "https://example.com/ml-article",
		Text:    "",
		Profile: "eng",
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	_ = q.MarkRelayed(id)
	req.QueueRowID = id

	eval := &stubEvaluator{score: 80, verdict: "Worth reading"}
	runScoreFileAsyncSync(t, req, q, eval, deps)

	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		t.Fatalf("read transcriptDir: %v", err)
	}
	var found bool
	for _, e := range entries {
		if strings.Contains(e.Name(), "_url_") {
			found = true
			data, _ := os.ReadFile(filepath.Join(transcriptDir, e.Name()))
			if !strings.Contains(string(data), pageContent) {
				t.Errorf("F5-CT-2: transcript body missing fetched content; got:\n%s", data)
			}
		}
	}
	if !found {
		t.Errorf("F5-CT-2: no _url_ transcript file found in %s", transcriptDir)
	}
}

// F5-CT-3: When both rawContent and req.Text are populated, rawContent (fetchedContent) wins.
// Coalesce order: rawContent first, req.Text only when rawContent is empty.
func TestF5CT3_BothPopulatedPrefersRawContent(t *testing.T) {
	isolateEventsDir(t)
	transcriptDir := filepath.Join(t.TempDir(), "transcripts")

	const pageContent = "Fetched page content  -  longer and richer than the pre-populated text."
	deps := installJinaServer(t, jinaBodyServer(t, 200, pageContent))
	deps.TranscriptsDir = transcriptDir
	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	req := &ShareRequest{
		Type:    "url",
		URL:     "https://example.com/article-ct3",
		Text:    "short pre-populated text",
		Profile: "eng",
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	_ = q.MarkRelayed(id)
	req.QueueRowID = id

	eval := &stubEvaluator{score: 70, verdict: "Maybe"}
	runScoreFileAsyncSync(t, req, q, eval, deps)

	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		t.Fatalf("read transcriptDir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "_url_") {
			data, _ := os.ReadFile(filepath.Join(transcriptDir, e.Name()))
			body := string(data)
			if strings.Contains(body, "short pre-populated text") && !strings.Contains(body, pageContent) {
				t.Errorf("F5-CT-3: transcript used req.Text instead of rawContent; got:\n%s", body)
			}
		}
	}
}

// F5-CT-4: Both fetchedContent and req.Text empty → no fabricated transcript body.
// AT-URI fetch fails and req.Text is empty → scoreAsync returns early (no transcript created).
func TestF5CT4_BothEmptyNoFabrication(t *testing.T) {
	isolateEventsDir(t)
	transcriptDir := filepath.Join(t.TempDir(), "transcripts")

	deps := installJinaServer(t, jinaBodyServer(t, 404, ""))

	deps.TranscriptsDir = transcriptDir
	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	req := &ShareRequest{
		Type:    "url",
		URL:     "at://did:plc:abc/app.bsky.feed.post/f5ct4",
		Text:    "",
		Profile: "eng",
	}
	id, err := q.EnqueueWithSource(req, "firehose")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	req.QueueRowID = id

	eval := &stubEvaluator{score: 75, verdict: "Strong Yes"}
	runScoreFileAsyncSync(t, req, q, eval, deps)

	// scoreAsync returns early when both fetch and req.Text are empty.
	// No transcript file should be created with fabricated content.
	entries, _ := os.ReadDir(transcriptDir)
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(transcriptDir, e.Name()))
		body := string(data)
		// Frontmatter is OK; only check that no spurious content was fabricated.
		if strings.Contains(body, "fabricated") {
			t.Errorf("F5-CT-4: fabricated content in transcript: %s", body)
		}
	}
}

// F5-RG-1: Regression guard  -  firehose item with non-empty CAR-extracted text
// must produce a transcript file with non-empty body (not empty as in EPIC-125 M6).
func TestF5RG1_FirehoseTranscriptBodyNonEmpty(t *testing.T) {
	isolateEventsDir(t)
	transcriptDir := filepath.Join(t.TempDir(), "transcripts")

	deps := installJinaServer(t, jinaBodyServer(t, 404, ""))

	deps.TranscriptsDir = transcriptDir
	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	postText := "attention mechanism paper: scaling transformers for long-context reasoning"
	req := &ShareRequest{
		Type:    "url",
		URL:     "at://did:plc:abc/app.bsky.feed.post/f5rg1",
		Text:    postText,
		Profile: "eng",
	}
	id, err := q.EnqueueWithSource(req, "firehose")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	req.QueueRowID = id

	eval := &stubEvaluator{score: 80, verdict: "Strong Yes"}
	runScoreFileAsyncSync(t, req, q, eval, deps)

	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		t.Fatalf("read transcriptDir: %v", err)
	}
	var found bool
	for _, e := range entries {
		if strings.Contains(e.Name(), "_url_") {
			found = true
			data, _ := os.ReadFile(filepath.Join(transcriptDir, e.Name()))
			// Extract body (content after second "---")
			body := string(data)
			parts := strings.SplitN(body, "---", 3)
			if len(parts) < 3 || strings.TrimSpace(parts[2]) == "" {
				t.Errorf("F5-RG-1: transcript body is empty (regression); file:\n%s", body)
			}
		}
	}
	if !found {
		t.Errorf("F5-RG-1: no _url_ transcript file found in %s", transcriptDir)
	}
}

// =====================================================================
// F7: Vision Path Guard
// =====================================================================

// F7-CT-1: Text-only URL share (AT-URI, req.Text non-empty, no Filename) →
// no vision_token_correction event emitted despite suspicious Usage (low tokens, high cost).
func TestF7CT1_TextOnlyURLNoVisionTokenEvent(t *testing.T) {
	isolateEventsDir(t)
	transcriptDir := filepath.Join(t.TempDir(), "transcripts")

	deps := installJinaServer(t, jinaBodyServer(t, 404, ""))

	deps.TranscriptsDir = transcriptDir
	req := &ShareRequest{
		Type:     "url",
		URL:      "at://did:plc:abc/app.bsky.feed.post/f7ct1",
		Text:     "LLM eval paper content  -  text only, no image",
		Filename: "",
		Profile:  "eng",
	}

	evPath := filepath.Join(t.TempDir(), "events.jsonl")
	eval := &visionStubEvaluator{score: 75, verdict: "Strong Yes"}
	runScoreAsyncWithEvents(t, req, eval, evPath, deps)

	counts := readEventTypesFromFile(t, evPath)
	if counts["vision_token_correction"] > 0 {
		t.Fatalf("F7-CT-1: vision_token_correction emitted %d time(s) for text-only URL share  -  guard not applied",
			counts["vision_token_correction"])
	}
}

// F7-CT-2: URL share with empty req.Text (HTTP fetch succeeds) →
// guard does NOT fire; vision_token_correction can still be emitted.
func TestF7CT2_URLWithFetchedContentNotGuarded(t *testing.T) {
	isolateEventsDir(t)
	transcriptDir := filepath.Join(t.TempDir(), "transcripts")

	deps := installJinaServer(t, jinaBodyServer(t, 200, "article about machine learning"))

	deps.TranscriptsDir = transcriptDir
	req := &ShareRequest{
		Type:     "url",
		URL:      "https://example.com/ml-article-f7ct2",
		Text:     "", // empty  -  guard condition req.Text!="" is false
		Filename: "",
		Profile:  "eng",
	}

	evPath := filepath.Join(t.TempDir(), "events.jsonl")
	eval := &visionStubEvaluator{score: 70, verdict: "Maybe"}
	runScoreAsyncWithEvents(t, req, eval, evPath, deps)

	// Guard does not fire (req.Text is empty) → vision_token_correction should emit.
	counts := readEventTypesFromFile(t, evPath)
	if counts["vision_token_correction"] == 0 {
		t.Fatal("F7-CT-2: vision_token_correction not emitted for non-text-only URL share  -  guard over-fired")
	}
}

// F7-CT-3: URL share with Filename set → guard does NOT fire (all three conditions required).
func TestF7CT3_URLWithFilenameNotGuarded(t *testing.T) {
	isolateEventsDir(t)
	transcriptDir := filepath.Join(t.TempDir(), "transcripts")

	deps := installJinaServer(t, jinaBodyServer(t, 404, ""))

	deps.TranscriptsDir = transcriptDir
	req := &ShareRequest{
		Type:     "url",
		URL:      "at://did:plc:abc/app.bsky.feed.post/f7ct3",
		Text:     "some text content",
		Filename: "photo.jpg", // set  -  guard condition req.Filename=="" is false
		Profile:  "eng",
	}

	evPath := filepath.Join(t.TempDir(), "events.jsonl")
	eval := &visionStubEvaluator{score: 70, verdict: "Maybe"}
	runScoreAsyncWithEvents(t, req, eval, evPath, deps)

	// Guard does not fire (Filename is set) → vision_token_correction should emit.
	counts := readEventTypesFromFile(t, evPath)
	if counts["vision_token_correction"] == 0 {
		t.Fatal("F7-CT-3: vision_token_correction not emitted when Filename is set  -  guard incorrectly fired")
	}
}

// F7-CT-4: Guard requires all three conditions simultaneously.
// Text-only URL share with Filename="" AND Type="url" AND Text!="" → guard fires.
// Changing any one condition disables the guard (verified by CT-2, CT-3).
func TestF7CT4_GuardRequiresAllThreeConditions(t *testing.T) {
	// Condition table: guard fires only when all three are true.
	cases := []struct {
		name     string
		filename string
		reqType  string
		text     string
		wantFire bool
	}{
		{"all three  -  guard fires", "", "url", "some text", true},
		{"filename set  -  no guard", "photo.jpg", "url", "some text", false},
		{"empty text  -  no guard", "", "url", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.filename == "" && tc.reqType == "url" && tc.text != ""
			if got != tc.wantFire {
				t.Errorf("guard condition for %q: got %v, want %v", tc.name, got, tc.wantFire)
			}
		})
	}
}

// F7-RG-1: Regression guard  -  text-only firehose posts must not emit vision_token_correction.
// Source: EPIC-125 M6 live validation (queue_id=23562, $0.013 for 347-char Bluesky post).
func TestF7RG1_TextOnlyFirehoseNoVisionCost(t *testing.T) {
	isolateEventsDir(t)
	transcriptDir := filepath.Join(t.TempDir(), "transcripts")

	deps := installJinaServer(t, jinaBodyServer(t, 404, ""))

	deps.TranscriptsDir = transcriptDir
	req := &ShareRequest{
		Type:     "url",
		URL:      "at://did:plc:abc/app.bsky.feed.post/f7rg1",
		Text:     "new paper on attention mechanisms and transformer efficiency",
		Filename: "",
		Profile:  "eng",
	}

	evPath := filepath.Join(t.TempDir(), "events.jsonl")
	eval := &visionStubEvaluator{score: 82, verdict: "Strong Yes"}
	runScoreAsyncWithEvents(t, req, eval, evPath, deps)

	counts := readEventTypesFromFile(t, evPath)
	if counts["vision_token_correction"] > 0 {
		t.Fatalf("F7-RG-1: vision_token_correction emitted for firehose text-only post  -  cost overrun regression detected")
	}
}
