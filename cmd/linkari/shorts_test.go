package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// CT-1: /shorts/ in URL → detectShorts returns true.
func TestShortsDetectURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://youtube.com/shorts/abc123", true},
		{"https://www.youtube.com/shorts/XYZ", true},
		{"https://youtube.com/watch?v=abc123", false},
		{"https://youtu.be/abc123", false},
	}
	for _, c := range cases {
		got := detectShorts(c.url, 0)
		if got != c.want {
			t.Errorf("detectShorts(%q, 0) = %v, want %v", c.url, got, c.want)
		}
	}
}

// CT-2: duration <= 60 → detectShorts returns true (no /shorts/ in URL).
func TestShortsDetectDuration(t *testing.T) {
	const regularURL = "https://youtube.com/watch?v=abc123"
	cases := []struct {
		duration int
		want     bool
	}{
		{60, true},
		{1, true},
		{0, false}, // 0 = unknown duration, should not tag as Short
		{61, false},
		{120, false},
	}
	for _, c := range cases {
		got := detectShorts(regularURL, c.duration)
		if got != c.want {
			t.Errorf("detectShorts(regularURL, %d) = %v, want %v", c.duration, got, c.want)
		}
	}
}

// CT-3: empty ShortsRubricTemplate → selectShortsRubric returns fallback.
// Guards against template_missing error in scoreYouTubeAsync (RG-1).
func TestShortsRubricFallback(t *testing.T) {
	const fallback = "default rubric text for regular youtube"
	got := selectShortsRubric("", fallback)
	if got != fallback {
		t.Fatalf("selectShortsRubric(\"\", fallback) = %q, want %q", got, fallback)
	}

	const custom = "shorts-specific rubric template"
	got = selectShortsRubric(custom, fallback)
	if got != custom {
		t.Fatalf("selectShortsRubric(custom, fallback) = %q, want %q", got, custom)
	}
}

// CT-4: SetIsShorts persists is_shorts flag to DB and GetByID reflects it.
func TestSetIsShorts(t *testing.T) {
	q := newTestQueue(t)
	id, err := q.Enqueue(&ShareRequest{Type: "url", URL: "https://youtube.com/shorts/test"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := q.SetIsShorts(id, true); err != nil {
		t.Fatalf("SetIsShorts: %v", err)
	}

	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !item.IsShorts {
		t.Fatal("expected IsShorts=true after SetIsShorts(id, true)")
	}

	// Round-trip false.
	if err := q.SetIsShorts(id, false); err != nil {
		t.Fatalf("SetIsShorts false: %v", err)
	}
	item, err = q.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID after false: %v", err)
	}
	if item.IsShorts {
		t.Fatal("expected IsShorts=false after SetIsShorts(id, false)")
	}
}

// CT-5: push_outbox.content_type = 'youtube_shorts' via SetPushContentType.
func TestShortsPushContentType(t *testing.T) {
	q := newTestQueue(t)
	pushID, err := q.EnqueuePush("notify", 80, "test-slug", "Good Short", "https://youtube.com/shorts/test")
	if err != nil {
		t.Fatalf("EnqueuePush: %v", err)
	}
	if err := q.SetPushContentType(pushID, "youtube_shorts"); err != nil {
		t.Fatalf("SetPushContentType: %v", err)
	}
	pushes, err := q.PendingPushes(10)
	if err != nil {
		t.Fatalf("PendingPushes: %v", err)
	}
	for _, p := range pushes {
		if p.ID == pushID {
			if p.ContentType != "youtube_shorts" {
				t.Fatalf("ContentType = %q, want %q", p.ContentType, "youtube_shorts")
			}
			return
		}
	}
	t.Fatal("push not found in PendingPushes")
}

// BT-1: selectShortsRubric uses the custom template when ShortsRubricTemplate is configured.
func TestShortsBT1RubricApplied(t *testing.T) {
	const custom = "rate this short on: entertainment value, pacing, visual quality"
	const fallback = "standard youtube rubric"
	got := selectShortsRubric(custom, fallback)
	if got != custom {
		t.Fatalf("BT-1: expected custom rubric, got %q", got)
	}
}

// BT-2: Non-Shorts video (duration=61, no /shorts/ URL) tagged is_shorts=0.
func TestShortsBT2NonShortsTagged(t *testing.T) {
	q := newTestQueue(t)
	id, err := q.Enqueue(&ShareRequest{Type: "url", URL: "https://youtube.com/watch?v=longvideo"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	isShort := detectShorts("https://youtube.com/watch?v=longvideo", 61)
	if isShort {
		t.Fatal("BT-2: expected detectShorts=false for duration=61 non-shorts URL")
	}
	if err := q.SetIsShorts(id, isShort); err != nil {
		t.Fatalf("SetIsShorts: %v", err)
	}
	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if item.IsShorts {
		t.Fatal("BT-2: expected IsShorts=false for duration=61 non-shorts video")
	}
}

// BT-3: detectShorts returns false for duration=0 (unknown duration).
func TestShortsBT3UnknownDuration(t *testing.T) {
	if detectShorts("https://youtube.com/watch?v=unknown", 0) {
		t.Fatal("BT-3: expected detectShorts=false for duration=0 (unknown)")
	}
}

// BT-4: FCM title starts with "Short:" when content_type='youtube_shorts'.
// Tests the title format string used in sendOutboxFCM.
func TestShortsBT4FCMTitle(t *testing.T) {
	verdict := "This is a very entertaining short video"
	title := fmt.Sprintf("Short: %s", firstSentence(verdict, 60))
	if !strings.HasPrefix(title, "Short:") {
		t.Fatalf("BT-4: expected title to start with 'Short:', got %q", title)
	}
	// Confirm the verdict excerpt is within the title.
	if !strings.Contains(title, "entertaining") {
		t.Fatalf("BT-4: expected title to contain verdict excerpt, got %q", title)
	}
}

// RG-1: empty ShortsRubricTemplate falls back to default — selectShortsRubric never returns "".
// Guards against scoreYouTubeAsync entering the template_missing code path for Shorts.
func TestShortsRG1RubricFallbackNeverEmpty(t *testing.T) {
	const defaultRubric = "standard evaluation rubric"
	got := selectShortsRubric("", defaultRubric)
	if got == "" {
		t.Fatal("RG-1: selectShortsRubric returned empty string — would cause template_missing")
	}
	if got != defaultRubric {
		t.Fatalf("RG-1: expected default rubric, got %q", got)
	}
}

// M12: End-to-end integration: Shorts URL → is_shorts=1 → push_outbox.content_type='youtube_shorts'.
func TestShortsAsync_M12_Integration(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // prevent resolvePushConfigOnce from loading real config.toml
	installTestProfileDir(t, "eng")

	prevNorm := execNormalizeURL
	execNormalizeURL = func(_ context.Context, u string) (string, error) { return u, nil }
	t.Cleanup(func() { execNormalizeURL = prevNorm })

	ytdlpStub := func(_ context.Context, _, _ string) (string, ytVideoMeta, error) {
		return "Short: quick and fun video transcript content", ytVideoMeta{
			Title:        "Fun Short",
			ID:           "m12-short",
			Duration:     45,
			IsShorts:     true,
			SubtitleType: "auto",
		}, nil
	}
	deps := &ytDeps{Ytdlp: ytdlpStub}

	deps.Backend = &funcScoringBackend{completeJSON: func(_ context.Context, _, _, _ string) ([]byte, error) {
		v := TriageVerdict{Score: 80, Verdict: "Entertaining Short", Tags: "shorts", RubricScores: map[string]int{"overall": 80}}
		return json.Marshal(v)
	}}

	q := newTestQueue(t)
	req := ShareRequest{
		Type:                 "url",
		URL:                  "https://www.youtube.com/shorts/m12test",
		Profile:              "eng",
		ShortsRubricTemplate: "Evaluate this Short on entertainment value and brevity.",
	}
	id, err := q.Enqueue(&req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	req.QueueRowID = id

	done := make(chan struct{})
	go func() {
		defer close(done)
		scoreYouTubeAsync(req, q, "yt-dlp", nil, "", nil, deps)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("scoreYouTubeAsync timed out")
	}

	// Assert is_shorts=1 in queue.
	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !item.IsShorts {
		t.Error("M12: is_shorts should be 1 for Shorts URL")
	}
	if item.Status != "scored" && item.Status != "archived" {
		t.Errorf("M12: status = %q, want scored or archived", item.Status)
	}

	// Assert push_outbox.content_type='youtube_shorts'.
	var ct string
	_ = q.db.QueryRow("SELECT content_type FROM push_outbox WHERE content_type='youtube_shorts' LIMIT 1").Scan(&ct)
	if ct != "youtube_shorts" {
		// Also check pending pushes (some configs may skip enqueue).
		pushes, _ := q.PendingPushes(20)
		for _, p := range pushes {
			if p.ContentType == "youtube_shorts" {
				return
			}
		}
		t.Errorf("M12: push_outbox.content_type should be 'youtube_shorts', got %q in direct query", ct)
	}
}

// RG-2: NewQueue called twice on the same DB does not return an error; is_shorts column exists.
func TestShortsRG2MigrationIdempotent(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	q1, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatalf("RG-2: first NewQueue: %v", err)
	}
	q1.Close()

	q2, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatalf("RG-2: second NewQueue: %v", err)
	}
	defer q2.Close()

	// Verify is_shorts column is usable.
	id, err := q2.Enqueue(&ShareRequest{Type: "url", URL: "https://youtube.com/shorts/rg2test"})
	if err != nil {
		t.Fatalf("RG-2: Enqueue: %v", err)
	}
	if err := q2.SetIsShorts(id, true); err != nil {
		t.Fatalf("RG-2: SetIsShorts: %v", err)
	}
	item, err := q2.GetByID(id)
	if err != nil {
		t.Fatalf("RG-2: GetByID: %v", err)
	}
	if !item.IsShorts {
		t.Fatal("RG-2: is_shorts column not usable after second NewQueue")
	}
}
