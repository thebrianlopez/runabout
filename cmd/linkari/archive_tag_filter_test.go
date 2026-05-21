package main

// EPIC-153: /archive?user_tag= filter contract tests.
// CT-1 through CT-5 assert the server-side tag filtering behavior for the
// Digest Tag Filtering feature (F4). Written before implementation per TDD.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newArchiveTagServer creates a minimal test server for archive tag filter tests.
func newArchiveTagServer(t *testing.T) (http.Handler, *Queue) {
	t.Helper()
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)
	return srv.Mux(), q
}

// getArchive calls GET /archive with the given query string and decodes items.
func getArchive(t *testing.T, mux http.Handler, query string) (int, []QueueItem) {
	t.Helper()
	path := "/archive"
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var items []QueueItem
	_ = json.NewDecoder(w.Body).Decode(&items)
	return w.Code, items
}

// seedTaggedArchiveItem enqueues, scores, archives, and tags an item.
// Returns the queue ID.
func seedTaggedArchiveItem(t *testing.T, q *Queue, url string, tags []string) int64 {
	t.Helper()
	id, err := q.Enqueue(&ShareRequest{URL: url, Type: "url"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := q.UpdateScore(id, 85, "", "verdict", "slug", "", ""); err != nil {
		t.Fatalf("score: %v", err)
	}
	if err := q.Archive(id); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if len(tags) > 0 {
		if err := q.persistUserTags(id, tags); err != nil {
			t.Fatalf("persistUserTags: %v", err)
		}
	}
	return id
}

// CT-1: GET /archive?user_tag=read-later returns only items where user_tags
// contains "read-later".
func TestArchiveTagFilter_CT1_FilteredItems(t *testing.T) {
	mux, q := newArchiveTagServer(t)

	id1 := seedTaggedArchiveItem(t, q, "https://a.example.com", []string{"read-later"})
	seedTaggedArchiveItem(t, q, "https://b.example.com", []string{"tech"})
	seedTaggedArchiveItem(t, q, "https://c.example.com", []string{"read-later", "ai"})

	code, items := getArchive(t, mux, "user_tag=read-later&status=archived")
	if code != http.StatusOK {
		t.Fatalf("CT-1: status = %d, want 200", code)
	}
	if len(items) != 2 {
		t.Fatalf("CT-1: got %d items, want 2 (only read-later tagged)", len(items))
	}
	for _, it := range items {
		if it.ID != id1 {
			// c.example.com also has read-later; verify neither b.example.com leaked
			found := false
			for _, item := range items {
				if item.URL == "https://c.example.com" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("CT-1: expected c.example.com in results")
			}
			break
		}
	}
	// Verify b.example.com (tech only) is absent.
	for _, it := range items {
		if it.URL == "https://b.example.com" {
			t.Errorf("CT-1: b.example.com (no read-later tag) should not appear in results")
		}
	}
}

// CT-2: GET /archive (no user_tag param) returns all archived items unfiltered.
func TestArchiveTagFilter_CT2_NoFilterReturnsAll(t *testing.T) {
	mux, q := newArchiveTagServer(t)

	seedTaggedArchiveItem(t, q, "https://a.example.com", []string{"read-later"})
	seedTaggedArchiveItem(t, q, "https://b.example.com", []string{"tech"})
	seedTaggedArchiveItem(t, q, "https://c.example.com", nil)

	code, items := getArchive(t, mux, "status=archived")
	if code != http.StatusOK {
		t.Fatalf("CT-2: status = %d, want 200", code)
	}
	if len(items) != 3 {
		t.Errorf("CT-2: got %d items, want 3 (all archived, no tag filter)", len(items))
	}
}

// CT-3: GET /archive?user_tag=read-later with zero matching items returns
// empty array (not 404).
func TestArchiveTagFilter_CT3_NoMatchReturnsEmpty(t *testing.T) {
	mux, q := newArchiveTagServer(t)

	// Seed items with different tags - none with "read-later".
	seedTaggedArchiveItem(t, q, "https://a.example.com", []string{"tech"})
	seedTaggedArchiveItem(t, q, "https://b.example.com", []string{"ai"})

	code, items := getArchive(t, mux, "user_tag=read-later&status=archived")
	if code != http.StatusOK {
		t.Fatalf("CT-3: status = %d, want 200 (empty array, not 404)", code)
	}
	if len(items) != 0 {
		t.Errorf("CT-3: got %d items, want 0 (no read-later tagged items exist)", len(items))
	}
}

// CT-4: user_tag composes with existing filters - profile and user_tag both filter.
func TestArchiveTagFilter_CT4_ComposesWithProfile(t *testing.T) {
	mux, q := newArchiveTagServer(t)

	// eng profile + read-later tag - should match.
	id1, err := q.Enqueue(&ShareRequest{URL: "https://eng-rl.example.com", Type: "url", Profile: "eng"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	q.UpdateScore(id1, 85, "", "verdict", "slug-eng-rl", "", "") //nolint:errcheck
	q.Archive(id1)                                               //nolint:errcheck
	q.persistUserTags(id1, []string{"read-later"})               //nolint:errcheck

	// life profile + read-later tag - should not match profile=eng filter.
	id2, err := q.Enqueue(&ShareRequest{URL: "https://life-rl.example.com", Type: "url", Profile: "life"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	q.UpdateScore(id2, 85, "", "verdict", "slug-life-rl", "", "") //nolint:errcheck
	q.Archive(id2)                                                //nolint:errcheck
	q.persistUserTags(id2, []string{"read-later"})                //nolint:errcheck

	// eng profile + tech tag - should not match user_tag=read-later filter.
	id3, err := q.Enqueue(&ShareRequest{URL: "https://eng-tech.example.com", Type: "url", Profile: "eng"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	q.UpdateScore(id3, 85, "", "verdict", "slug-eng-tech", "", "") //nolint:errcheck
	q.Archive(id3)                                                 //nolint:errcheck
	q.persistUserTags(id3, []string{"tech"})                       //nolint:errcheck

	code, items := getArchive(t, mux, "profile=eng&user_tag=read-later&status=archived")
	if code != http.StatusOK {
		t.Fatalf("CT-4: status = %d, want 200", code)
	}
	if len(items) != 1 {
		t.Fatalf("CT-4: got %d items, want 1 (only eng profile + read-later tag)", len(items))
	}
	if items[0].ID != id1 {
		t.Errorf("CT-4: got item id=%d url=%s, want id=%d (eng+read-later)", items[0].ID, items[0].URL, id1)
	}
}

// CT-5: GET /archive?user_tag=nonexistent with NULL user_tags rows - those
// rows do not appear in the result (json_each(NULL) returns zero rows).
func TestArchiveTagFilter_CT5_NullUserTagsDoNotMatch(t *testing.T) {
	mux, q := newArchiveTagServer(t)

	// Item with no user_tags (NULL column) - should not match any tag filter.
	id1, err := q.Enqueue(&ShareRequest{URL: "https://no-tags.example.com", Type: "url"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	q.UpdateScore(id1, 85, "", "verdict", "slug", "", "") //nolint:errcheck
	q.Archive(id1)                                        //nolint:errcheck
	// Deliberately do NOT call persistUserTags - user_tags column remains NULL.

	code, items := getArchive(t, mux, "user_tag=nonexistent&status=archived")
	if code != http.StatusOK {
		t.Fatalf("CT-5: status = %d, want 200", code)
	}
	if len(items) != 0 {
		t.Errorf("CT-5: got %d items, want 0 (NULL user_tags rows must not match tag filter)", len(items))
	}
}
