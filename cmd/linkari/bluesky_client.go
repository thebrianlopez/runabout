package main

// EPIC-013: Bluesky AT Protocol authentication client.
//
// Implements the minimal AT Protocol session creation/refresh needed to
// authenticate with Bluesky. No external library — uses standard HTTP + JSON.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

const bskyDefaultHost = "https://bsky.social"

// BlueskySessionData holds the credentials from a successful AT Protocol login.
// Stored as JSON in users.bluesky_session_json.
type BlueskySessionData struct {
	DID        string `json:"did"`
	Handle     string `json:"handle"`
	AccessJWT  string `json:"access_jwt"`
	RefreshJWT string `json:"refresh_jwt"`
	Host       string `json:"host"`
}

// BlueskyRefreshCallback is called by BlueskyClient when tokens are refreshed.
type BlueskyRefreshCallback func(data BlueskySessionData) error

// BlueskyClient wraps an active AT Protocol session.
type BlueskyClient struct {
	Session  BlueskySessionData
	onRefresh BlueskyRefreshCallback
}

// AccountDID returns the authenticated user's DID.
func (c *BlueskyClient) AccountDID() string {
	if c == nil {
		return ""
	}
	return c.Session.DID
}

// host returns the configured AT Protocol host, falling back to bsky.social.
func (s BlueskySessionData) host() string {
	if s.Host != "" {
		return s.Host
	}
	return bskyDefaultHost
}

// bskyCreateSessionReq is the AT Protocol createSession request body.
type bskyCreateSessionReq struct {
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
}

// bskyCreateSessionResp is the AT Protocol createSession response body.
type bskyCreateSessionResp struct {
	DID        string `json:"did"`
	Handle     string `json:"handle"`
	AccessJwt  string `json:"accessJwt"`
	RefreshJwt string `json:"refreshJwt"`
	Error      string `json:"error,omitempty"`
	Message    string `json:"message,omitempty"`
}

// LoginBluesky authenticates with the AT Protocol server and returns a client.
func LoginBluesky(ctx context.Context, host, handle, password string) (*BlueskyClient, error) {
	if host == "" {
		host = bskyDefaultHost
	}
	endpoint := host + "/xrpc/com.atproto.server.createSession"

	body, err := json.Marshal(bskyCreateSessionReq{Identifier: handle, Password: password})
	if err != nil {
		return nil, fmt.Errorf("marshal createSession: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("createSession: %w", err)
	}
	defer resp.Body.Close()

	var result bskyCreateSessionResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode createSession response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bluesky_auth_failed: %s — %s", result.Error, result.Message)
	}

	return &BlueskyClient{
		Session: BlueskySessionData{
			DID:        result.DID,
			Handle:     result.Handle,
			AccessJWT:  result.AccessJwt,
			RefreshJWT: result.RefreshJwt,
			Host:       host,
		},
	}, nil
}

// ResumeBlueskySession constructs a BlueskyClient from persisted session data
// without making a network call. The refreshCb is stored for use on refresh.
func ResumeBlueskySession(data BlueskySessionData, refreshCb BlueskyRefreshCallback) *BlueskyClient {
	return &BlueskyClient{Session: data, onRefresh: refreshCb}
}

// requireBskyClient returns (client, true) when bskyClient is set. When nil,
// it logs bluesky_session_missing and returns (nil, false). Use as a guard in
// F2/F3/F4 handlers that depend on an authenticated Bluesky session (EPIC-013 M7).
func (s *Server) requireBskyClient(caller string) (*BlueskyClient, bool) {
	if s.bskyClient != nil {
		return s.bskyClient, true
	}
	slog.Warn("bluesky session missing",
		"event_type", "bluesky_session_missing",
		"caller", caller,
	)
	return nil, false
}

// resumeBlueskySessionsOnStartup loads any persisted Bluesky session for
// user_id=1 (single-user deployment) and wires it onto s.bskyClient.
func resumeBlueskySessionsOnStartup(ctx context.Context, q *Queue, s *Server) {
	data, err := q.LoadBlueskySession(1)
	if err != nil {
		slog.Warn("bluesky session load failed on startup",
			"event_type", "bluesky_session_corrupt",
			"user_id", 1,
			"error", err,
		)
		return
	}
	if data == nil {
		return // no session persisted
	}
	refreshCb := func(updated BlueskySessionData) error {
		return q.UpdateBlueskySession(1, updated)
	}
	s.bskyClient = ResumeBlueskySession(*data, refreshCb)
	slog.Info("bluesky session resumed",
		"event_type", "bluesky_session_resumed",
		"user_id", 1,
		"account_did", data.DID,
	)
}
