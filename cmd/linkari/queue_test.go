package main

import (
	"fmt"
	"path/filepath"
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
