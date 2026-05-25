package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var deviceIDRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func suffix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func validateDeviceID(id string) error {
	if id == "" {
		return errors.New("device_id_missing")
	}
	if !deviceIDRE.MatchString(id) {
		return errors.New("device_id_invalid")
	}
	return nil
}

func requireSessionUser(s *Server, w http.ResponseWriter, r *http.Request) (int64, bool) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return 0, false
	}
	uid, ok := s.checkSessionAuth(strings.TrimPrefix(auth, "Bearer "))
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return 0, false
	}
	return uid, true
}

type deviceRegisterRequest struct {
	DeviceID   string `json:"device_id"`
	FCMToken   string `json:"fcm_token"`
	DeviceName string `json:"device_name"`
	Platform   string `json:"platform"`
	AppVersion string `json:"app_version"`
}
type deviceRegisterResponse struct {
	DeviceID     string `json:"device_id"`
	Registered   bool   `json:"registered"`
	Enabled      bool   `json:"enabled"`
	TokenUpdated bool   `json:"token_updated"`
}
type deviceInfo struct {
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name,omitempty"`
	Platform   string `json:"platform"`
	AppVersion string `json:"app_version,omitempty"`
	Enabled    bool   `json:"enabled"`
	UpdatedAt  int64  `json:"updated_at"`
	LastSeenAt int64  `json:"last_seen_at"`
}

func (s *Server) handleRegisterDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	uid, ok := requireSessionUser(s, w, r)
	if !ok {
		return
	}
	var req deviceRegisterRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.DeviceID = strings.TrimSpace(req.DeviceID)
	req.FCMToken = strings.TrimSpace(req.FCMToken)
	if err := validateDeviceID(req.DeviceID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.FCMToken == "" {
		writeError(w, http.StatusBadRequest, "fcm_token_missing")
		return
	}
	if len(req.FCMToken) > 4096 {
		writeError(w, http.StatusBadRequest, "fcm_token_invalid")
		return
	}
	if req.Platform == "" {
		req.Platform = "android"
	}
	tokenUpdated, err := s.queue.RegisterDevice(r.Context(), uid, req)
	if err != nil {
		slog.Error("register_device_db_error", "error", err, "user_id", uid, "device_id", req.DeviceID, "platform", req.Platform)
		writeError(w, http.StatusServiceUnavailable, "device_registry_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, deviceRegisterResponse{DeviceID: req.DeviceID, Registered: true, Enabled: true, TokenUpdated: tokenUpdated})
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	uid, ok := requireSessionUser(s, w, r)
	if !ok {
		return
	}
	devices, err := s.queue.ListDevices(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "device_registry_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

func (s *Server) handleDisableDevice(w http.ResponseWriter, r *http.Request) {
	uid, ok := requireSessionUser(s, w, r)
	if !ok {
		return
	}
	id := r.PathValue("device_id")
	if err := validateDeviceID(id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.queue.DisableDevice(r.Context(), uid, id); err != nil {
		writeError(w, http.StatusServiceUnavailable, "device_registry_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device_id": id, "enabled": false})
}

func (q *Queue) RegisterDevice(ctx context.Context, userID int64, req deviceRegisterRequest) (bool, error) {
	now := time.Now().Unix()
	platform := req.Platform
	if platform == "" {
		platform = "android"
	}
	var old string
	err := q.db.QueryRowContext(ctx, `SELECT token FROM devices WHERE user_id=? AND device_id=?`, userID, req.DeviceID).Scan(&old)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	tokenUpdated := err == sql.ErrNoRows || old != req.FCMToken

	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	// FCM tokens are globally unique (legacy schema uses token as PRIMARY KEY),
	// while Android app reinstall / data-clear can generate a new device_id for
	// the same token. Reassign the token before the (user_id, device_id) upsert
	// so registration remains idempotent instead of surfacing a 503.
	if _, err = tx.ExecContext(ctx, `DELETE FROM devices WHERE token=? AND NOT (user_id=? AND device_id=?)`, req.FCMToken, userID, req.DeviceID); err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO devices(token, updated_at, user_id, device_id, device_name, platform, app_version, enabled, created_at, token_updated_at, last_seen_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(user_id, device_id) DO UPDATE SET token=excluded.token, updated_at=excluded.updated_at, device_name=excluded.device_name, platform=excluded.platform, app_version=excluded.app_version, enabled=1, token_updated_at=CASE WHEN token<>excluded.token THEN excluded.token_updated_at ELSE token_updated_at END, last_seen_at=excluded.last_seen_at`,
		req.FCMToken, now, userID, req.DeviceID, req.DeviceName, platform, req.AppVersion, 1, now, now, now)
	if err != nil {
		return false, err
	}
	return tokenUpdated, tx.Commit()
}

func (q *Queue) LookupDeviceToken(ctx context.Context, userID int64, deviceID string) (string, error) {
	var token string
	err := q.db.QueryRowContext(ctx, `SELECT token FROM devices WHERE user_id=? AND device_id=? AND enabled=1 LIMIT 1`, userID, deviceID).Scan(&token)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return token, err
}

func (q *Queue) DeviceBelongsToUser(ctx context.Context, userID int64, deviceID string) (bool, error) {
	var one int
	err := q.db.QueryRowContext(ctx, `SELECT 1 FROM devices WHERE user_id=? AND device_id=? LIMIT 1`, userID, deviceID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (q *Queue) ListDevices(ctx context.Context, userID int64) ([]deviceInfo, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT device_id, COALESCE(device_name,''), COALESCE(platform,'android'), COALESCE(app_version,''), COALESCE(enabled,1), COALESCE(updated_at,0), COALESCE(last_seen_at,0) FROM devices WHERE user_id=? AND COALESCE(device_id,'')<>'' ORDER BY updated_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []deviceInfo
	for rows.Next() {
		var d deviceInfo
		var en int
		if err := rows.Scan(&d.DeviceID, &d.DeviceName, &d.Platform, &d.AppVersion, &en, &d.UpdatedAt, &d.LastSeenAt); err != nil {
			return nil, err
		}
		d.Enabled = en != 0
		out = append(out, d)
	}
	return out, rows.Err()
}

func (q *Queue) DisableDevice(ctx context.Context, userID int64, deviceID string) error {
	_, err := q.db.ExecContext(ctx, `UPDATE devices SET enabled=0, updated_at=? WHERE user_id=? AND device_id=?`, time.Now().Unix(), userID, deviceID)
	return err
}
