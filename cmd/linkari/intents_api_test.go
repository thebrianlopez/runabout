package main

// EPIC-157 M1: GET /intents API server contract tests.
// CT-1, CT-2, CT-6, CT-7 assert server-side behavior.
// Written before implementation (TDD gate).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// CT-1: GET /intents returns exactly 2 items: score and capture.
func TestIntentsAPI_CT1_ReturnsTwoItems(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	req := httptest.NewRequest(http.MethodGet, "/intents", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleIntents(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-1: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp IntentsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("CT-1: decode response: %v", err)
	}
	if len(resp.Intents) != 2 {
		t.Errorf("CT-1: len(intents) = %d, want 2", len(resp.Intents))
	}

	ids := make([]string, 0, 2)
	for _, item := range resp.Intents {
		ids = append(ids, item.ID)
	}
	if !containsStr(ids, "score") || !containsStr(ids, "capture") {
		t.Errorf("CT-1: intent IDs = %v, want [score capture]", ids)
	}
}

// CT-2: transcribe must not appear in GET /intents response.
func TestIntentsAPI_CT2_TranscribeNotInResponse(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	req := httptest.NewRequest(http.MethodGet, "/intents", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleIntents(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-2: expected 200, got %d", w.Code)
	}

	var resp IntentsResponse
	json.NewDecoder(w.Body).Decode(&resp)
	for _, item := range resp.Intents {
		if item.ID == "transcribe" {
			t.Error("CT-2: transcribe must not appear in GET /intents response (RG-2)")
		}
	}
}

// CT-6: GET /actions returns valid legacy response.
func TestIntentsAPI_CT6_ActionsLegacyResponse(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	req := httptest.NewRequest(http.MethodGet, "/actions", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleActions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-6: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("CT-6: content-type = %q, want application/json", ct)
	}
	// Response must decode as some valid JSON (non-empty, not null).
	var raw json.RawMessage
	if err := json.NewDecoder(w.Body).Decode(&raw); err != nil {
		t.Fatalf("CT-6: response is not valid JSON: %v", err)
	}
}

// CT-7: POST /share accepts both intent and action; intent wins.
func TestIntentsAPI_CT7_IntentWinsOverAction(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	// Send both intent=capture and action=uinit_eng; intent must win.
	body := `{"type":"url","url":"https://ct7.example.com","intent":"capture","action":"uinit_eng"}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-7: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp ShareResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.ID == 0 {
		t.Fatal("CT-7: expected non-zero queue ID")
	}

	var intent string
	q.db.QueryRow(`SELECT intent FROM queue WHERE id = ?`, resp.ID).Scan(&intent)
	if intent != "capture" {
		t.Errorf("CT-7: intent = %q, want capture (intent must win over action)", intent)
	}
}

// BT-3: GET /actions emits action_compat_used counter.
// Verifies the counter is incremented on each GET /actions call.
func TestIntentsAPI_BT3_ActionsEmitsCompatCounter(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	before := actionCompatUsedTotal.Load()

	req := httptest.NewRequest(http.MethodGet, "/actions", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleActions(w, req)

	after := actionCompatUsedTotal.Load()
	if after != before+1 {
		t.Errorf("BT-3: action_compat_used counter: before=%d, after=%d, want before+1", before, after)
	}
}

func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
