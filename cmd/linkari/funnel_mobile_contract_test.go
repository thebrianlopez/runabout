package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFunnelMuxMobileContractRoutes(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	srv := NewServer("test-token", router, newTestQueue(t), NewRingLog(10), false, nil)
	// Use log mode so shield does not mask route availability.
	srv.SetShield(NewShield("log"))

	handler := srv.FunnelMux()

	t.Run("intents route is exposed over funnel", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/intents", nil)
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("X-Linkari-Client", "android/1.0.0/cloud")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Fatalf("GET /intents on FunnelMux returned 404; mobile contract route missing")
		}
	})

	t.Run("device registration route is exposed over funnel", func(t *testing.T) {
		body := bytes.NewBufferString(`{"device_id":"android-funnel-contract","fcm_token":"fcm-test-token","platform":"android"}`)
		req := httptest.NewRequest(http.MethodPost, "/devices/register", body)
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Linkari-Client", "android/1.0.0/cloud")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Fatalf("POST /devices/register on FunnelMux returned 404; mobile contract route missing")
		}
	})

	t.Run("insight report route is exposed over funnel", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/analytics/share-tags/report?window=7d", nil)
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("X-Linkari-Client", "android/1.0.0/cloud")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Fatalf("GET /analytics/share-tags/report on FunnelMux returned 404; mobile contract route missing")
		}
	})
}
