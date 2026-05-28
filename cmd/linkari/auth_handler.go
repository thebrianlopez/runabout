package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// authGoogleRequest is the JSON body for POST /auth/google.
type authGoogleRequest struct {
	IDToken string `json:"id_token"`
}

// authGoogleResponse is returned by POST /auth/google.
type authGoogleResponse struct {
	Status       string `json:"status"`                  // "authenticated" or "invite_required"
	SessionToken string `json:"session_token,omitempty"` // present when authenticated
	Email        string `json:"email,omitempty"`         // always present on valid token
	Name         string `json:"name,omitempty"`          // from Google profile
	UserID       int64  `json:"user_id,omitempty"`       // present when authenticated
}

// authInviteRequest is the JSON body for POST /auth/invite.
type authInviteRequest struct {
	IDToken    string `json:"id_token"`
	InviteCode string `json:"invite_code"`
}

// adminInviteResponse is returned by POST /admin/invite.
type adminInviteResponse struct {
	Code string `json:"code"`
}

// handleAuthGoogle exchanges a Google ID token for a session token.
// Unknown users receive {status: "invite_required"}.
func (s *Server) handleAuthGoogle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.googleVerifier == nil {
		writeError(w, http.StatusServiceUnavailable, "google sign-in not configured")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxPayloadSize)
	var req authGoogleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.IDToken == "" {
		writeError(w, http.StatusBadRequest, "id_token required")
		return
	}

	claims, err := s.googleVerifier.Verify(req.IDToken)
	if err != nil {
		slog.Warn("google token verification failed", "error", err)
		writeError(w, http.StatusUnauthorized, "invalid id_token")
		return
	}

	// Look up user by google_sub.
	user, err := s.queue.LookupUserBySub(claims.Sub)
	if err != nil {
		slog.Error("user lookup failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if user == nil {
		// Unknown user — needs an invite code.
		writeJSON(w, http.StatusOK, authGoogleResponse{
			Status: "invite_required",
			Email:  claims.Email,
			Name:   claims.Name,
		})
		return
	}

	// Known user — issue session token.
	token, err := s.issueSession(user.ID, claims.Sub)
	if err != nil {
		slog.Error("session creation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, authGoogleResponse{
		Status:       "authenticated",
		SessionToken: token,
		Email:        claims.Email,
		Name:         claims.Name,
		UserID:       user.ID,
	})
}

// handleAuthInvite redeems an invite code and creates a new user account.
func (s *Server) handleAuthInvite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.googleVerifier == nil {
		writeError(w, http.StatusServiceUnavailable, "google sign-in not configured")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxPayloadSize)
	var req authInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.IDToken == "" || req.InviteCode == "" {
		writeError(w, http.StatusBadRequest, "id_token and invite_code required")
		return
	}

	// Re-verify the Google token.
	claims, err := s.googleVerifier.Verify(req.IDToken)
	if err != nil {
		slog.Warn("google token verification failed", "error", err)
		writeError(w, http.StatusUnauthorized, "invalid id_token")
		return
	}

	// Check if user already exists.
	existing, err := s.queue.LookupUserBySub(claims.Sub)
	if err != nil {
		slog.Error("user lookup failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if existing != nil {
		// User already exists — just issue a session.
		token, err := s.issueSession(existing.ID, claims.Sub)
		if err != nil {
			slog.Error("session creation failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, authGoogleResponse{
			Status:       "authenticated",
			SessionToken: token,
			Email:        claims.Email,
			Name:         claims.Name,
			UserID:       existing.ID,
		})
		return
	}

	// Redeem the invite code and create the user atomically.
	userID, err := s.queue.RedeemInvite(req.InviteCode, claims.Sub, claims.Email, claims.Name)
	if err != nil {
		slog.Warn("invite redemption failed", "error", err, "code", req.InviteCode)
		writeError(w, http.StatusBadRequest, "invalid or expired invite code")
		return
	}

	token, err := s.issueSession(userID, claims.Sub)
	if err != nil {
		slog.Error("session creation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	slog.Info(
		"new user created via invite",
		"user_id", userID,
		"email", claims.Email,
		"invite_code", req.InviteCode,
	)

	writeJSON(w, http.StatusOK, authGoogleResponse{
		Status:       "authenticated",
		SessionToken: token,
		Email:        claims.Email,
		Name:         claims.Name,
		UserID:       userID,
	})
}

// handleAdminInvite generates a new invite code. Requires the operator bearer token.
func (s *Server) handleAdminInvite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Admin-only: requires the operator bearer token (same as /share).
	auth := r.Header.Get("Authorization")
	bearer := strings.TrimPrefix(auth, "Bearer ")
	if !strings.HasPrefix(auth, "Bearer ") || bearer != s.token {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	code, err := s.queue.CreateInviteCode()
	if err != nil {
		slog.Error("invite code creation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	slog.Info("invite code created", "code", code)
	writeJSON(w, http.StatusOK, adminInviteResponse{Code: code})
}

// issueSession creates a session token for the given user.
func (s *Server) issueSession(userID int64, googleSub string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)

	ttlDays := 90
	if s.sessionTTLDays > 0 {
		ttlDays = s.sessionTTLDays
	}
	expiresAt := time.Now().Add(time.Duration(ttlDays) * 24 * time.Hour)

	if err := s.queue.InsertSession(token, userID, googleSub, expiresAt); err != nil {
		return "", err
	}
	return token, nil
}

// authBlueskyRequest is the JSON body for POST /auth/bluesky. EPIC-013 M4.
type authBlueskyRequest struct {
	Handle   string `json:"handle"`
	Password string `json:"password"`
	Host     string `json:"host,omitempty"`
}

// authBlueskyResponse is the JSON response for a successful Bluesky login.
type authBlueskyResponse struct {
	Status     string `json:"status"`
	AccountDID string `json:"account_did"`
}

// handleAuthBluesky authenticates a Bluesky account and persists the session.
// POST /auth/bluesky requires a valid Linkari session token (same as other
// user-scoped endpoints). EPIC-013 M4/M7.
func (s *Server) handleAuthBluesky(w http.ResponseWriter, r *http.Request) {
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	userID, ok := s.checkSessionAuth(bearer)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req authBlueskyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	client, err := LoginBluesky(r.Context(), req.Host, req.Handle, req.Password)
	if err != nil {
		slog.Warn(
			"bluesky auth failed",
			"event_type", "bluesky_auth_failed",
			"user_id", userID,
			"error_class", "login_failed",
			"host", req.Host,
		)
		http.Error(w, "bluesky_auth_failed", http.StatusUnauthorized)
		return
	}

	refreshCb := func(updated BlueskySessionData) error {
		slog.Debug(
			"bluesky token refreshed",
			"event_type", "bluesky_token_refreshed",
			"user_id", userID,
			"account_did", updated.DID,
		)
		return s.queue.UpdateBlueskySession(userID, updated)
	}
	client.onRefresh = refreshCb

	if err := s.queue.PersistBlueskySession(userID, client.Session); err != nil {
		slog.Warn("bluesky session persist failed", "user_id", userID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.bskyClient = client

	slog.Info(
		"bluesky auth connected",
		"event_type", "bluesky_auth_connected",
		"user_id", userID,
		"account_did", client.Session.DID,
		"host", client.Session.Host,
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(authBlueskyResponse{Status: "connected", AccountDID: client.Session.DID})
}

// checkSessionAuth looks up a bearer token in the sessions table.
// Returns the user ID and true if the session is valid and not expired.
func (s *Server) checkSessionAuth(bearer string) (userID int64, ok bool) {
	if s.queue == nil {
		return 0, false
	}
	uid, err := s.queue.LookupSession(bearer)
	if err != nil {
		return 0, false
	}
	return uid, true
}
