package main

// EPIC-150 M1: Share-time tag inventory API contract tests.
// CT-1 through CT-5 assert the desired behavior of GET /tags.
// Depends on EPIC-149 (F2 tags table must exist).
// Written before implementation (TDD); tests compile and pass once M2 lands.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// seedTagInventory inserts rows directly into the tags table, bypassing
// persistUserTags so tests can control use_count and last_used_at precisely.
func seedTagInventory(t *testing.T, q *Queue, name string, useCount int, lastUsedAt time.Time) {
	t.Helper()
	ts := lastUsedAt.UTC().Format(time.RFC3339)
	_, err := q.db.Exec(
		`INSERT INTO tags (name, use_count, last_used_at, created_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET use_count=excluded.use_count, last_used_at=excluded.last_used_at`,
		name, useCount, ts, ts,
	)
	if err != nil {
		t.Fatalf("seedTagInventory(%q, %d): %v", name, useCount, err)
	}
}

// CT-1: GET /tags returns tag inventory sorted by combined recency/frequency score.
func TestGetTags_CT1_InventorySorted(t *testing.T) {
	q := newTestQueue(t)
	now := time.Now()
	seedTagInventory(t, q, "tech", 10, now)
	seedTagInventory(t, q, "ai", 5, now.Add(-1*time.Hour))
	seedTagInventory(t, q, "go", 1, now.Add(-24*time.Hour))

	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	req := httptest.NewRequest(http.MethodGet, "/tags", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-1: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp TagsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("CT-1: decode response: %v", err)
	}
	if len(resp.Tags) < 3 {
		t.Fatalf("CT-1: expected 3 tags, got %d", len(resp.Tags))
	}
	// "tech" has highest use_count and recent timestamp  -  must rank first.
	if resp.Tags[0].Name != "tech" {
		t.Errorf("CT-1: expected tags[0]=tech (highest freq+recency), got %q", resp.Tags[0].Name)
	}
	// All tags must have required fields.
	for i, tag := range resp.Tags {
		if tag.Name == "" {
			t.Errorf("CT-1: tags[%d] missing name", i)
		}
		if tag.UseCount < 1 {
			t.Errorf("CT-1: tags[%d] use_count = %d, want >= 1", i, tag.UseCount)
		}
		if tag.LastUsedAt == "" {
			t.Errorf("CT-1: tags[%d] missing last_used_at", i)
		}
	}
}

// CT-2: Empty inventory returns empty array, not 404 or null.
func TestGetTags_CT2_EmptyInventory(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	req := httptest.NewRequest(http.MethodGet, "/tags", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-2: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp TagsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("CT-2: decode response: %v", err)
	}
	if resp.Tags == nil {
		t.Error("CT-2: tags field must be an empty array, not null")
	}
	if len(resp.Tags) != 0 {
		t.Errorf("CT-2: expected 0 tags, got %d", len(resp.Tags))
	}
}

// CT-3: limit query parameter caps the number of results.
func TestGetTags_CT3_LimitCaps(t *testing.T) {
	q := newTestQueue(t)
	now := time.Now()
	for i := 1; i <= 10; i++ {
		seedTagInventory(t, q, string(rune('a'+i-1)), i, now.Add(time.Duration(-i)*time.Minute))
	}

	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	req := httptest.NewRequest(http.MethodGet, "/tags?limit=3", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-3: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp TagsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("CT-3: decode response: %v", err)
	}
	if len(resp.Tags) != 3 {
		t.Errorf("CT-3: expected 3 tags with limit=3, got %d", len(resp.Tags))
	}
}

// CT-4: Auth required  -  401 without Bearer token.
func TestGetTags_CT4_AuthRequired(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	req := httptest.NewRequest(http.MethodGet, "/tags", nil)
	// No Authorization header.
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("CT-4: expected 401, got %d", w.Code)
	}
}

// CT-5: Ranking prefers frequent+recent over infrequent+old.
// "popular" (high use_count, recent) must rank above "obscure" (low use_count, old).
func TestGetTags_CT5_RankingFreqAndRecency(t *testing.T) {
	q := newTestQueue(t)
	now := time.Now()
	seedTagInventory(t, q, "popular", 100, now)
	seedTagInventory(t, q, "obscure", 1, now.Add(-30*24*time.Hour))

	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	req := httptest.NewRequest(http.MethodGet, "/tags", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.Mux().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-5: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp TagsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("CT-5: decode response: %v", err)
	}
	if len(resp.Tags) < 2 {
		t.Fatalf("CT-5: expected 2 tags, got %d", len(resp.Tags))
	}
	if resp.Tags[0].Name != "popular" {
		t.Errorf("CT-5: expected popular to rank first, got %q", resp.Tags[0].Name)
	}
}
