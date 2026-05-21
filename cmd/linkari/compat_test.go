package main

// EPIC-161 F8: Backward Compatibility + Deprecation Soak contract tests.
// CT-1 through CT-11 assert action-field compat and ?profile= translation.
// Written before implementation (TDD gate).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// CT-1: POST /share with action="uinit_eng" (no intent) routes to score/domain:eng.
func TestCompat_CT1_ActionUinitEngRoutesScore(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	body := `{"type":"url","url":"https://ct1-compat.example.com","action":"uinit_eng"}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-1: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp ShareResponse
	json.NewDecoder(w.Body).Decode(&resp)

	var intent string
	q.db.QueryRow(`SELECT intent FROM queue WHERE id = ?`, resp.ID).Scan(&intent)
	if intent != "score" {
		t.Errorf("CT-1: intent = %q, want score (action uinit_eng -> score)", intent)
	}
}

// CT-2: POST /share with action="ginit_eng" routes to capture/jira.
// ginit actions require the Jira token; set one so the scope check passes.
func TestCompat_CT2_ActionGinitEngRoutesCapture(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)
	srv.jiraToken = "jira-test-token" // needed for ginit_* scope check

	body := `{"type":"url","url":"https://ct2-compat.example.com","action":"ginit_eng","text":"ENG-123"}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer jira-test-token")
	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-2: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp ShareResponse
	json.NewDecoder(w.Body).Decode(&resp)

	var intent string
	q.db.QueryRow(`SELECT intent FROM queue WHERE id = ?`, resp.ID).Scan(&intent)
	if intent != "capture" {
		t.Errorf("CT-2: intent = %q, want capture (action ginit_eng -> capture)", intent)
	}
}

// CT-3: POST /share with action="vnote_auto" routes to transcribe.
func TestCompat_CT3_ActionVnoteRoutesTranscribe(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	body := `{"type":"url","url":"https://ct3-compat.example.com","action":"vnote_auto"}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-3: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp ShareResponse
	json.NewDecoder(w.Body).Decode(&resp)

	var intent string
	q.db.QueryRow(`SELECT intent FROM queue WHERE id = ?`, resp.ID).Scan(&intent)
	if intent != "transcribe" {
		t.Errorf("CT-3: intent = %q, want transcribe (action vnote_auto -> transcribe)", intent)
	}
}

// CT-4: intent field wins when both intent and action present (already covered by F4 CT-7).
func TestCompat_CT4_IntentWinsOverAction(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	body := `{"type":"url","url":"https://ct4-compat.example.com","intent":"capture","action":"uinit_eng"}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("CT-4: expected 200, got %d", w.Code)
	}
	var resp ShareResponse
	json.NewDecoder(w.Body).Decode(&resp)

	var intent string
	q.db.QueryRow(`SELECT intent FROM queue WHERE id = ?`, resp.ID).Scan(&intent)
	if intent != "capture" {
		t.Errorf("CT-4: intent = %q, want capture (intent wins over action)", intent)
	}
}

// CT-7: action_compat_used emitted on action-field use but NOT on intent-field-only use.
func TestCompat_CT7_CompatCounterOnActionFieldOnly(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	before := actionCompatUsedTotal.Load()

	// Share with action only - should increment counter.
	body := `{"type":"url","url":"https://ct7a-compat.example.com","action":"uinit_eng"}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	afterAction := actionCompatUsedTotal.Load()
	if afterAction != before+1 {
		t.Errorf("CT-7: compat counter after action-only share: got %d, want %d", afterAction, before+1)
	}

	// Share with intent only - must NOT increment counter.
	body2 := `{"type":"url","url":"https://ct7b-compat.example.com","intent":"score"}`
	req2 := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer test-token")
	w2 := httptest.NewRecorder()
	srv.handleShare(w2, req2)

	afterIntent := actionCompatUsedTotal.Load()
	if afterIntent != afterAction {
		t.Errorf("CT-7: compat counter must NOT increment on intent-only share: got %d, want %d", afterIntent, afterAction)
	}
}

// CT-8: deriveIntentFromAction covers all 7 uinit_ profiles.
func TestCompat_CT8_DeriveIntentAllUinitProfiles(t *testing.T) {
	cases := []struct {
		action string
		intent string
		tagSig string
	}{
		{"uinit_eng", "score", "domain:eng"},
		{"uinit_life", "score", "domain:personal"},
		{"uinit_travel", "score", "domain:travel"},
		{"uinit_fashion", "score", "domain:fashion"},
		{"uinit_music", "score", "domain:music"},
		{"uinit_finance", "score", "domain:finance"},
		{"uinit_dining", "score", "domain:dining"},
	}
	for _, tc := range cases {
		intent, tagSig, ok := deriveIntentFromAction(tc.action)
		if !ok {
			t.Errorf("CT-8: deriveIntentFromAction(%q) ok=false", tc.action)
			continue
		}
		if intent != tc.intent {
			t.Errorf("CT-8: %q intent = %q, want %q", tc.action, intent, tc.intent)
		}
		if tagSig != tc.tagSig {
			t.Errorf("CT-8: %q tagSig = %q, want %q", tc.action, tagSig, tc.tagSig)
		}
	}
}

// CT-9: deriveIntentFromAction covers ginit_ prefix.
func TestCompat_CT9_DeriveIntentGinitPrefix(t *testing.T) {
	intent, tagSig, ok := deriveIntentFromAction("ginit_anything")
	if !ok {
		t.Fatal("CT-9: ok=false for ginit_anything")
	}
	if intent != "capture" {
		t.Errorf("CT-9: intent = %q, want capture", intent)
	}
	if tagSig != "jira" {
		t.Errorf("CT-9: tagSig = %q, want jira", tagSig)
	}
}

// CT-10: deriveIntentFromAction covers vnote_ prefix.
func TestCompat_CT10_DeriveIntentVnotePrefix(t *testing.T) {
	intent, tagSig, ok := deriveIntentFromAction("vnote_anything")
	if !ok {
		t.Fatal("CT-10: ok=false for vnote_anything")
	}
	if intent != "transcribe" {
		t.Errorf("CT-10: intent = %q, want transcribe", intent)
	}
	if tagSig != "" {
		t.Errorf("CT-10: tagSig = %q, want empty", tagSig)
	}
}

// CT-11: Unrecognized action defaults to score safely.
func TestCompat_CT11_UnrecognizedActionDefaultsScore(t *testing.T) {
	intent, _, ok := deriveIntentFromAction("unknown_xyz")
	// ok=false is correct for unrecognized actions.
	if ok {
		t.Error("CT-11: ok=true for unknown action; expected false (unrecognized)")
	}
	if intent != "score" {
		t.Errorf("CT-11: intent = %q, want score (safe default)", intent)
	}
}

// RG-1: Old Android client POST with action only must not 400.
func TestCompat_RG1_ActionOnlyNotRejected(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	body := `{"type":"url","url":"https://rg1-compat.example.com","action":"uinit_eng"}`
	req := httptest.NewRequest(http.MethodPost, "/share", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.handleShare(w, req)

	if w.Code == http.StatusBadRequest {
		t.Errorf("RG-1: POST with action-only returned 400; old Android clients must not be broken")
	}
}
