package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestQueue(t *testing.T) *Queue {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	q, err := NewQueue(dbPath, false)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	t.Cleanup(func() { q.Close() })
	return q
}

// TestNewQueueCorruptDB verifies that NewQueue returns a descriptive error
// rather than silently continuing when the SQLite file is corrupt.
// Regression guard for the 2026-04-13 incident (SQLITE_CORRUPT 11) where
// corruption was only surfaced during "seed invite codes" and the server
// accepted traffic with all DB-backed endpoints returning 500.
func TestNewQueueCorruptDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "corrupt.db")
	// Write garbage bytes that look nothing like a valid SQLite database.
	if err := os.WriteFile(dbPath, []byte("this is not a sqlite database\x00\xff\xfe"), 0o600); err != nil {
		t.Fatalf("write corrupt db: %v", err)
	}
	_, err := NewQueue(dbPath, false)
	if err == nil {
		t.Fatal("expected error for corrupt database, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "corrupt") && !strings.Contains(msg, "integrity") && !strings.Contains(msg, "malformed") {
		t.Errorf("error message %q should mention corruption or integrity", msg)
	}
}

func TestQueueSnapshot(t *testing.T) {
	q := newTestQueue(t)

	// Enqueue a row so the snapshot is non-trivial.
	if _, err := q.Enqueue(&ShareRequest{Type: "url", URL: "https://snap.test"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	destPath := filepath.Join(t.TempDir(), "queue.db.bak")
	if err := q.Snapshot(destPath); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Open the snapshot and verify the row is present.
	snap, err := NewQueue(destPath, false)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer snap.Close()

	items, err := snap.Pending()
	if err != nil {
		t.Fatalf("snapshot Pending: %v", err)
	}
	if len(items) != 1 || items[0].URL != "https://snap.test" {
		t.Errorf("snapshot contents unexpected: %+v", items)
	}
}

func TestQueueEnqueueAndPending(t *testing.T) {
	q := newTestQueue(t)

	urls := []string{"https://a.com", "https://b.com", "https://c.com"}
	for _, u := range urls {
		if _, err := q.Enqueue(&ShareRequest{Type: "url", URL: u}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	items, err := q.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 pending, got %d", len(items))
	}
	// FIFO order
	for i, it := range items {
		if it.URL != urls[i] {
			t.Errorf("item[%d].URL = %q, want %q", i, it.URL, urls[i])
		}
		if it.Status != "pending" {
			t.Errorf("item[%d].Status = %q, want pending", i, it.Status)
		}
	}
}

func TestQueueMarkRelayed(t *testing.T) {
	q := newTestQueue(t)

	id, err := q.Enqueue(&ShareRequest{Type: "url", URL: "https://test.com"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := q.MarkRelayed(id); err != nil {
		t.Fatalf("MarkRelayed: %v", err)
	}

	pending, err := q.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("expected 0 pending after MarkRelayed, got %d", len(pending))
	}

	// Should appear in list with status=relayed
	all, err := q.List("relayed", 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 || all[0].Status != "relayed" {
		t.Errorf("expected 1 relayed item, got %+v", all)
	}
}

func TestQueuePrune(t *testing.T) {
	q := newTestQueue(t)

	for i := 0; i < 205; i++ {
		if _, err := q.Enqueue(&ShareRequest{Type: "url", URL: "https://example.com"}); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}

	if err := q.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	all, err := q.List("", 300)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) > maxQueueSize {
		t.Errorf("expected <= %d items after prune, got %d", maxQueueSize, len(all))
	}
}

func TestQueueList(t *testing.T) {
	q := newTestQueue(t)

	// Enqueue 2 items, mark one as relayed
	id1, _ := q.Enqueue(&ShareRequest{Type: "url", URL: "https://a.com", Profile: "eng"})
	q.Enqueue(&ShareRequest{Type: "url", URL: "https://b.com", Profile: "life"})
	q.MarkRelayed(id1)

	// Filter by status
	pending, _ := q.List("pending", 10)
	if len(pending) != 1 {
		t.Errorf("expected 1 pending, got %d", len(pending))
	}

	relayed, _ := q.List("relayed", 10)
	if len(relayed) != 1 {
		t.Errorf("expected 1 relayed, got %d", len(relayed))
	}

	// All items
	all, _ := q.List("", 10)
	if len(all) != 2 {
		t.Errorf("expected 2 total, got %d", len(all))
	}
}

func TestQueueUpdateScore(t *testing.T) {
	q := newTestQueue(t)

	id, _ := q.Enqueue(&ShareRequest{Type: "url", URL: "https://test.com", Profile: "eng"})
	q.MarkRelayed(id)

	if err := q.UpdateScore(id, 87, "networking,vpn", "good article about vpn tools", "test-slug"); err != nil {
		t.Fatalf("UpdateScore: %v", err)
	}

	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if item.Status != "scored" {
		t.Errorf("status = %q, want scored", item.Status)
	}
	if item.Score == nil || *item.Score != 87 {
		t.Errorf("score = %v, want 87", item.Score)
	}
	if item.Tags != "networking,vpn" {
		t.Errorf("tags = %q, want networking,vpn", item.Tags)
	}
	if item.ScoredAt == "" {
		t.Error("scored_at should be set")
	}
}

func TestQueueArchive(t *testing.T) {
	q := newTestQueue(t)

	id, _ := q.Enqueue(&ShareRequest{Type: "url", URL: "https://test.com", Profile: "eng"})
	q.MarkRelayed(id)
	q.UpdateScore(id, 92, "go,tools", "", "")
	q.Archive(id)

	archived, err := q.ListArchived("", 10)
	if err != nil {
		t.Fatalf("ListArchived: %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("expected 1 archived, got %d", len(archived))
	}
	if archived[0].Status != "archived" {
		t.Errorf("status = %q, want archived", archived[0].Status)
	}
	if archived[0].ArchivedAt == "" {
		t.Error("archived_at should be set")
	}

	// Filter by profile
	engOnly, _ := q.ListArchived("eng", 10)
	if len(engOnly) != 1 {
		t.Errorf("expected 1 eng archived, got %d", len(engOnly))
	}
	lifeOnly, _ := q.ListArchived("life", 10)
	if len(lifeOnly) != 0 {
		t.Errorf("expected 0 life archived, got %d", len(lifeOnly))
	}
}

func TestQueueRecentScored(t *testing.T) {
	q := newTestQueue(t)

	scores := []int{45, 92, 73, 88, 61}
	for i, s := range scores {
		id, _ := q.Enqueue(&ShareRequest{Type: "url", URL: fmt.Sprintf("https://%d.com", i)})
		q.MarkRelayed(id)
		q.UpdateScore(id, s, "", "", "")
	}

	since := time.Now().Add(-1 * time.Hour)
	items, err := q.RecentScored(since, 10)
	if err != nil {
		t.Fatalf("RecentScored: %v", err)
	}
	if len(items) != 5 {
		t.Fatalf("expected 5, got %d", len(items))
	}
	// Should be ranked by score descending
	if items[0].Score == nil || *items[0].Score != 92 {
		t.Errorf("first item score = %v, want 92", items[0].Score)
	}
	if items[4].Score == nil || *items[4].Score != 45 {
		t.Errorf("last item score = %v, want 45", items[4].Score)
	}
}

func TestFTS5Available(t *testing.T) {
	q := newTestQueue(t)
	_, err := q.db.Exec("INSERT INTO queue_fts(queue_fts) VALUES('integrity-check')")
	if err != nil {
		t.Fatalf("FTS5 integrity check failed: %v", err)
	}
}

func TestQueueScoreByURL_UpdateRelayed(t *testing.T) {
	q := newTestQueue(t)

	id, _ := q.Enqueue(&ShareRequest{Type: "url", URL: "https://example.com/article", Profile: "eng"})
	q.MarkRelayed(id)

	item, inserted, err := q.ScoreByURL("https://example.com/article", 85, "great article", "networking", "eng", "example-article")
	if err != nil {
		t.Fatalf("ScoreByURL: %v", err)
	}
	if inserted {
		t.Error("expected update (inserted=false), got insert")
	}
	if item.Status != "scored" {
		t.Errorf("status = %q, want scored", item.Status)
	}
	if item.Score == nil || *item.Score != 85 {
		t.Errorf("score = %v, want 85", item.Score)
	}
	if item.Verdict != "great article" {
		t.Errorf("verdict = %q, want %q", item.Verdict, "great article")
	}
	if item.Slug != "example-article" {
		t.Errorf("slug = %q, want %q", item.Slug, "example-article")
	}
}

func TestQueueScoreByURL_Insert(t *testing.T) {
	q := newTestQueue(t)

	item, inserted, err := q.ScoreByURL("https://cli-only.com", 72, "cli verdict", "", "finance", "cli-only-slug")
	if err != nil {
		t.Fatalf("ScoreByURL: %v", err)
	}
	if !inserted {
		t.Error("expected insert (inserted=true), got update")
	}
	if item.Status != "scored" {
		t.Errorf("status = %q, want scored", item.Status)
	}
	if item.Profile != "finance" {
		t.Errorf("profile = %q, want finance", item.Profile)
	}
}

func TestQueueScoreByURL_Idempotent(t *testing.T) {
	q := newTestQueue(t)

	q.ScoreByURL("https://already.com", 90, "first verdict", "", "eng", "slug1")
	item, inserted, err := q.ScoreByURL("https://already.com", 95, "second verdict", "", "eng", "slug2")
	if err != nil {
		t.Fatalf("ScoreByURL: %v", err)
	}
	if inserted {
		t.Error("expected no insert on duplicate URL")
	}
	// Should return the existing item with the original score.
	if item.Score == nil || *item.Score != 90 {
		t.Errorf("score = %v, want 90 (original)", item.Score)
	}

	// Verify only 1 row exists for this URL.
	all, _ := q.List("", 100)
	count := 0
	for _, it := range all {
		if it.URL == "https://already.com" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 row for URL, got %d", count)
	}
}

func TestQueueSearchFTS5(t *testing.T) {
	q := newTestQueue(t)

	q.ScoreByURL("https://example.com/pangolin", 88, "self-hosted tunnel alternative to cloudflare", "networking", "eng", "pangolin")
	q.ScoreByURL("https://example.com/recipe", 65, "delicious pasta carbonara recipe from rome", "food", "dining", "pasta-recipe")

	// Search for tunnel-related content.
	results, err := q.SearchFTS5("tunnel", "", 10)
	if err != nil {
		t.Fatalf("SearchFTS5: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'tunnel', got %d", len(results))
	}
	if results[0].Slug != "pangolin" {
		t.Errorf("slug = %q, want pangolin", results[0].Slug)
	}

	// Search with profile filter.
	diningResults, err := q.SearchFTS5("recipe", "dining", 10)
	if err != nil {
		t.Fatalf("SearchFTS5 dining: %v", err)
	}
	if len(diningResults) != 1 {
		t.Errorf("expected 1 dining result, got %d", len(diningResults))
	}

	// Search with non-matching profile filter.
	engResults, err := q.SearchFTS5("recipe", "eng", 10)
	if err != nil {
		t.Fatalf("SearchFTS5 eng: %v", err)
	}
	if len(engResults) != 0 {
		t.Errorf("expected 0 eng results for 'recipe', got %d", len(engResults))
	}
}

func TestQueueVerdictSlugRoundTrip(t *testing.T) {
	q := newTestQueue(t)

	id, _ := q.Enqueue(&ShareRequest{Type: "url", URL: "https://test.com", Profile: "eng"})
	q.MarkRelayed(id)
	q.UpdateScore(id, 80, "go", "verdict text here", "my-slug")

	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if item.Verdict != "verdict text here" {
		t.Errorf("verdict = %q, want %q", item.Verdict, "verdict text here")
	}
	if item.Slug != "my-slug" {
		t.Errorf("slug = %q, want %q", item.Slug, "my-slug")
	}
}

// EPIC-051 M2: EnqueueDigestIfDue tests.

func TestEnqueueDigestIfDue_HappyPath(t *testing.T) {
	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	res, err := q.EnqueueDigestIfDue(testCtx(), "eng", 90, "slug-1", "verdict", "https://a.com")
	if err != nil {
		t.Fatalf("EnqueueDigestIfDue: %v", err)
	}
	if !res.Enqueued || res.Reason != "enqueued" || res.ID == 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestEnqueueDigestIfDue_ThrottledWithinWindow(t *testing.T) {
	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	if _, err := q.EnqueueDigestIfDue(testCtx(), "eng", 90, "slug-1", "", ""); err != nil {
		t.Fatalf("first: %v", err)
	}
	res, err := q.EnqueueDigestIfDue(testCtx(), "eng", 91, "slug-2", "", "")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if res.Enqueued || res.Reason != "throttled" {
		t.Fatalf("expected throttled, got %+v", res)
	}
	if res.SecondsUntilAllowed <= 0 {
		t.Fatalf("expected positive SecondsUntilAllowed, got %d", res.SecondsUntilAllowed)
	}
}

func TestEnqueueDigestIfDue_PerProfileIndependent(t *testing.T) {
	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Hour})

	if _, err := q.EnqueueDigestIfDue(testCtx(), "eng", 90, "a", "", ""); err != nil {
		t.Fatalf("eng: %v", err)
	}
	res, err := q.EnqueueDigestIfDue(testCtx(), "dining", 90, "b", "", "")
	if err != nil {
		t.Fatalf("dining: %v", err)
	}
	if !res.Enqueued {
		t.Fatalf("dining should be enqueued independently of eng: %+v", res)
	}
}

func TestEnqueueDigestIfDue_BoundaryExactWindow(t *testing.T) {
	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{DigestThrottleDefault: time.Second})

	if _, err := q.EnqueueDigestIfDue(testCtx(), "eng", 90, "a", "", ""); err != nil {
		t.Fatalf("first: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	res, err := q.EnqueueDigestIfDue(testCtx(), "eng", 90, "b", "", "")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !res.Enqueued {
		t.Fatalf("expected boundary enqueue to succeed: %+v", res)
	}
}

func TestEnqueueDigestIfDue_EmptyOutboxFirstCall(t *testing.T) {
	q := newTestQueue(t)
	res, err := q.EnqueueDigestIfDue(testCtx(), "eng", 90, "slug", "", "")
	if err != nil {
		t.Fatalf("EnqueueDigestIfDue: %v", err)
	}
	if !res.Enqueued {
		t.Fatalf("first call on empty outbox should enqueue: %+v", res)
	}
}

func TestEnqueueDigestIfDue_BelowMinScore(t *testing.T) {
	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{NotifyMinScore: 70, DigestThrottleDefault: time.Hour})

	res, err := q.EnqueueDigestIfDue(testCtx(), "eng", 40, "slug", "", "")
	if err != nil {
		t.Fatalf("EnqueueDigestIfDue: %v", err)
	}
	if res.Enqueued || res.Reason != "below_min_score" {
		t.Fatalf("expected below_min_score, got %+v", res)
	}
}

func TestEnqueueDigestIfDue_PerProfileOverride(t *testing.T) {
	q := newTestQueue(t)
	q.SetPushConfig(&PushConfig{
		DigestThrottleDefault: time.Hour,
		DigestThrottle: map[string]time.Duration{
			"dining": time.Nanosecond,
		},
	})

	if _, err := q.EnqueueDigestIfDue(testCtx(), "dining", 90, "a", "", ""); err != nil {
		t.Fatalf("first: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	res, err := q.EnqueueDigestIfDue(testCtx(), "dining", 90, "b", "", "")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !res.Enqueued {
		t.Fatalf("dining with ns throttle should not suppress: %+v", res)
	}
}

func testCtx() context.Context { return context.Background() }

func TestClassifySkipReason(t *testing.T) {
	tests := []struct {
		score   int
		verdict string
		want    string
	}{
		{85, "Great technical article", ""},
		{0, "", ""},
		{0, "Paywalled content behind login", "paywalled"},
		{0, "No content available on this page", "no_content"},
		{0, "Empty page with no meaningful content", "no_content"},
		{0, "Not technical — lifestyle blog post", "not_technical"},
		{0, "Non-technical entertainment content", "not_technical"},
		{0, "Song lyrics for popular track", "song_lyrics"},
		{0, "Duplicate of previously scored URL", "duplicate"},
		{0, "Login required to view this page", "login_required"},
		{0, "Authentication required", "login_required"},
		{0, "404 page not found", "not_found"},
		{0, "Generic low quality content", "skipped"},
	}
	for _, tt := range tests {
		got := classifySkipReason(tt.score, tt.verdict)
		if got != tt.want {
			t.Errorf("classifySkipReason(%d, %q) = %q, want %q", tt.score, tt.verdict, got, tt.want)
		}
	}
}

func TestSkipReasonInArchiveResponse(t *testing.T) {
	q := newTestQueue(t)
	id, err := q.Enqueue(&ShareRequest{URL: "https://example.com/paywalled", Type: "url", Profile: "eng"})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.UpdateScore(id, 0, "", "Paywalled content behind subscription", "slug"); err != nil {
		t.Fatal(err)
	}
	items, err := q.ListArchived("", 10)
	// score=0 items are "scored" not "archived", use List instead
	scored, err := q.List("scored", 10)
	if err != nil {
		t.Fatal(err)
	}
	_ = items
	if len(scored) == 0 {
		t.Fatal("expected scored items")
	}
	found := false
	for _, it := range scored {
		if it.URL == "https://example.com/paywalled" {
			found = true
			if it.SkipReason != "paywalled" {
				t.Errorf("skip_reason = %q, want %q", it.SkipReason, "paywalled")
			}
		}
	}
	if !found {
		t.Error("paywalled item not found in scored list")
	}
}

// --- EPIC-070 tests ---

func TestUpdateOutcome(t *testing.T) {
	q := newTestQueue(t)
	id, _ := q.Enqueue(&ShareRequest{Type: "url", URL: "https://outcome.test"})
	q.UpdateScore(id, 80, "go", "good article", "slug-1")

	if err := q.UpdateOutcome(id, "acted"); err != nil {
		t.Fatalf("UpdateOutcome: %v", err)
	}

	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if item.Outcome != "acted" {
		t.Errorf("outcome = %q, want %q", item.Outcome, "acted")
	}
	if item.OutcomeAt == "" {
		t.Error("outcome_at should be set")
	}
}

func TestUpdateOutcomeValidation(t *testing.T) {
	q := newTestQueue(t)
	id, _ := q.Enqueue(&ShareRequest{Type: "url", URL: "https://outcome-val.test"})

	if err := q.UpdateOutcome(id, "invalid"); err == nil {
		t.Error("expected error for invalid outcome")
	}
	if err := q.UpdateOutcome(id, ""); err == nil {
		t.Error("expected error for empty outcome")
	}
}

func TestUpdateFeedback(t *testing.T) {
	q := newTestQueue(t)
	id, _ := q.Enqueue(&ShareRequest{Type: "url", URL: "https://feedback.test"})
	q.UpdateScore(id, 75, "go", "decent", "slug-2")

	if err := q.UpdateFeedback(id, "too_high"); err != nil {
		t.Fatalf("UpdateFeedback: %v", err)
	}

	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if item.Feedback != "too_high" {
		t.Errorf("feedback = %q, want %q", item.Feedback, "too_high")
	}
	if item.FeedbackAt == "" {
		t.Error("feedback_at should be set")
	}
}

func TestUpdateFeedbackValidation(t *testing.T) {
	q := newTestQueue(t)
	id, _ := q.Enqueue(&ShareRequest{Type: "url", URL: "https://feedback-val.test"})

	if err := q.UpdateFeedback(id, "wrong"); err == nil {
		t.Error("expected error for invalid feedback")
	}
}

func TestProfileStats(t *testing.T) {
	q := newTestQueue(t)

	// Seed items across two profiles.
	for i, u := range []string{"https://a.test", "https://b.test", "https://c.test"} {
		id, _ := q.Enqueue(&ShareRequest{Type: "url", URL: u, Profile: "work"})
		q.UpdateScore(id, 60+i*10, "go", "v", fmt.Sprintf("slug-%d", i))
		q.Archive(id)
	}
	id4, _ := q.Enqueue(&ShareRequest{Type: "url", URL: "https://d.test", Profile: "life"})
	q.UpdateScore(id4, 90, "go", "great", "slug-d")
	q.Archive(id4)

	// Add feedback to some items.
	q.UpdateFeedback(1, "accurate")
	q.UpdateFeedback(2, "too_high")

	stats, err := q.ProfileStats("")
	if err != nil {
		t.Fatalf("ProfileStats: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(stats))
	}

	// Test single profile filter.
	stats, err = q.ProfileStats("work")
	if err != nil {
		t.Fatalf("ProfileStats(work): %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(stats))
	}
	if stats[0].Count != 3 {
		t.Errorf("count = %d, want 3", stats[0].Count)
	}
	if stats[0].FeedbackCount != 2 {
		t.Errorf("feedback_count = %d, want 2", stats[0].FeedbackCount)
	}
}

func TestListArchivedFiltered(t *testing.T) {
	q := newTestQueue(t)

	// Seed items with varied scores.
	for i, u := range []string{"https://f1.test", "https://f2.test", "https://f3.test"} {
		id, _ := q.Enqueue(&ShareRequest{Type: "url", URL: u, Profile: "work"})
		q.UpdateScore(id, 50+i*20, "go", "v", fmt.Sprintf("s-%d", i)) // 50, 70, 90
		q.Archive(id)
	}

	// Filter: score_min=60 → should return items with score 70 and 90.
	min := 60
	items, err := q.ListArchivedCursorTyped("", "", "", 0, 50, &ArchiveFilter{ScoreMin: &min})
	if err != nil {
		t.Fatalf("ListArchivedCursorTyped: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items with score>=60, got %d", len(items))
	}

	// Filter: score_max=60 → should return item with score 50.
	max := 60
	items, err = q.ListArchivedCursorTyped("", "", "", 0, 50, &ArchiveFilter{ScoreMax: &max})
	if err != nil {
		t.Fatalf("ListArchivedCursorTyped: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item with score<=60, got %d", len(items))
	}

	// Filter: since (future date) → should return 0.
	items, err = q.ListArchivedCursorTyped("", "", "", 0, 50, &ArchiveFilter{Since: "2099-01-01T00:00:00Z"})
	if err != nil {
		t.Fatalf("ListArchivedCursorTyped since: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items since future, got %d", len(items))
	}

	// No filter → all 3.
	items, err = q.ListArchivedCursorTyped("", "", "", 0, 50, nil)
	if err != nil {
		t.Fatalf("ListArchivedCursorTyped nil filter: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items with no filter, got %d", len(items))
	}
}
