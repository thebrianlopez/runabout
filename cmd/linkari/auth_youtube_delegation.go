package main

// EPIC-184: YouTube Account Delegation — POST /auth/youtube serverAuthCode exchange.
// Receives a one-time serverAuthCode from the Android client, exchanges it for a
// refresh_token via server-to-server POST to Google's token endpoint, and stores
// the credential in youtube_oauth_slots keyed by the authenticated user_id.
//
// The redirect_uri for Android serverAuthCode flows is "postmessage" — a Google-
// specified sentinel value, not a real URL. The GCP OAuth client must be type
// "Android" (matched by package name + SHA-1 at runtime); the web-application
// client is used only for the server-side token exchange.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// serverAuthCodeExchanger is the injectable interface for Google OAuth token exchange.
// The real implementation calls oauth2.googleapis.com/token.
// The test implementation returns controlled responses without network calls.
type serverAuthCodeExchanger interface {
	Exchange(ctx context.Context, code, clientID, clientSecret string) (refreshToken string, expiresAt int64, err error)
}

// googleAuthCodeExchanger is the production implementation of serverAuthCodeExchanger.
type googleAuthCodeExchanger struct{}

func (g *googleAuthCodeExchanger) Exchange(ctx context.Context, code, clientID, clientSecret string) (string, int64, error) {
	vals := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {"postmessage"}, // Google sentinel for Android serverAuthCode flows
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://oauth2.googleapis.com/token",
		strings.NewReader(vals.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("token exchange request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("google token endpoint %d: %s", resp.StatusCode, string(body))
	}

	var tok struct {
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", 0, fmt.Errorf("parse token response: %w", err)
	}
	if tok.RefreshToken == "" {
		return "", 0, fmt.Errorf("token exchange returned no refresh_token")
	}

	expiresAt := time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).Unix()
	return tok.RefreshToken, expiresAt, nil
}

// youTubeAuthRequest is the JSON body for POST /auth/youtube.
type youTubeAuthRequest struct {
	ServerAuthCode string `json:"server_auth_code"`
	Slot           string `json:"slot"`
}

// handleYouTubeAuth exchanges a serverAuthCode for a refresh token and stores it
// in youtube_oauth_slots for the authenticated user. EPIC-184 F1.
func (s *Server) handleYouTubeAuth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Require session token auth — resolves user_id from the sessions table.
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	bearer := strings.TrimPrefix(auth, "Bearer ")
	userID, ok := s.checkSessionAuth(bearer)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	// Decode request body.
	var req youTubeAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ServerAuthCode == "" {
		slog.WarnContext(ctx, "youtube_delegation_missing_code", "user_id", userID)
		writeError(w, http.StatusBadRequest, "missing_server_auth_code")
		return
	}
	slot := req.Slot
	if slot == "" {
		slot = "personal"
	}

	// Exchange serverAuthCode → refresh_token (server-to-server, no browser redirect).
	exchanger := s.authCodeExchanger
	if exchanger == nil {
		exchanger = &googleAuthCodeExchanger{}
	}
	refreshToken, expiresAt, err := exchanger.Exchange(ctx, req.ServerAuthCode, s.googleClientID, s.googleClientSecret)
	if err != nil {
		slog.ErrorContext(ctx, "youtube_delegation_exchange_failed",
			"slot", slot, "user_id", userID, "detail", err.Error())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{
			"error":  "token_exchange_failed",
			"detail": err.Error(),
		})
		return
	}

	// Store in youtube_oauth_slots with source="android".
	existing, _, _ := s.queue.GetYouTubeSlotToken(userID, slot)
	if err := s.queue.SetYouTubeSlotTokenWithSource(userID, slot, refreshToken, expiresAt, "android"); err != nil {
		slog.ErrorContext(ctx, "youtube_delegation_store_failed",
			"slot", slot, "user_id", userID, "err", err)
		writeError(w, http.StatusInternalServerError, "store_failed")
		return
	}

	if existing != "" {
		slog.InfoContext(ctx, "youtube_delegation_overwrite", "slot", slot, "user_id", userID)
	} else {
		slog.InfoContext(ctx, "youtube_delegation_stored", "slot", slot, "user_id", userID, "source", "android")
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"slot":   slot,
		"status": "stored",
	})
}
