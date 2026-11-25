package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMuxMobileContractRoutes is the Android contract retained from the
// funnel_mobile_contract_test (F11). Since EPIC-272 collapsed FunnelMux into
// Mux(), these mobile-contract routes live on the single serving mux.
func TestMuxMobileContractRoutes(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	srv := NewServer("test-token", router, newTestQueue(t), NewRingLog(10), false, nil)
	// Use log mode so shield does not mask route availability.
	srv.SetShield(NewShield("log"))

	handler := srv.Mux()

	t.Run("intents route is exposed on mux", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/intents", nil)
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("X-Linkari-Client", "android/1.0.0/cloud")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Fatalf("GET /intents on Mux returned 404; mobile contract route missing")
		}
	})

	t.Run("device registration route is exposed on mux", func(t *testing.T) {
		body := bytes.NewBufferString(`{"device_id":"android-mux-contract","fcm_token":"fcm-test-token","platform":"android"}`)
		req := httptest.NewRequest(http.MethodPost, "/devices/register", body)
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Linkari-Client", "android/1.0.0/cloud")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Fatalf("POST /devices/register on Mux returned 404; mobile contract route missing")
		}
	})

	t.Run("insight report route is exposed on mux", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/analytics/share-tags/report?window=7d", nil)
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("X-Linkari-Client", "android/1.0.0/cloud")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Fatalf("GET /analytics/share-tags/report on Mux returned 404; mobile contract route missing")
		}
	})
}
