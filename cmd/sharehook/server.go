package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// maxPayloadSize limits request body to 64KB.
const maxPayloadSize = 64 * 1024

// ShareRequest is the incoming payload from Android HTTP Shortcuts.
type ShareRequest struct {
	Type    string `json:"type"`
	Action  string `json:"action,omitempty"`
	Text    string `json:"text,omitempty"`
	URL     string `json:"url,omitempty"`
	Target  string `json:"target,omitempty"`
	Enter   bool   `json:"enter"`
	Profile string `json:"profile,omitempty"`
}

// ShareResponse is the structured JSON response.
type ShareResponse struct {
	Status    string `json:"status"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

// RingLog is a thread-safe ring buffer that captures log lines and
// fans out new lines to SSE subscribers.
type RingLog struct {
	mu    sync.Mutex
	lines []string
	max   int
	subs  map[chan string]struct{}
}

// NewRingLog creates a ring buffer that keeps the last n log lines.
func NewRingLog(n int) *RingLog {
	return &RingLog{max: n, subs: make(map[chan string]struct{})}
}

// Write implements io.Writer so RingLog can be used with log.SetOutput.
func (r *RingLog) Write(p []byte) (int, error) {
	line := string(p)
	r.mu.Lock()
	r.lines = append(r.lines, line)
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
	// Fan out to subscribers (non-blocking).
	for ch := range r.subs {
		select {
		case ch <- line:
		default:
		}
	}
	r.mu.Unlock()
	return len(p), nil
}

// Lines returns a copy of the buffered log lines.
func (r *RingLog) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	return out
}

// Subscribe returns a channel that receives new log lines.
func (r *RingLog) Subscribe() chan string {
	ch := make(chan string, 64)
	r.mu.Lock()
	r.subs[ch] = struct{}{}
	r.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel.
func (r *RingLog) Unsubscribe(ch chan string) {
	r.mu.Lock()
	delete(r.subs, ch)
	r.mu.Unlock()
	close(ch)
}

// Writer returns an io.Writer that fans out to both stdout and the ring buffer.
func (r *RingLog) Writer() io.Writer {
	return io.MultiWriter(os.Stdout, r)
}

// Server handles HTTP requests with authentication and rate limiting.
type Server struct {
	token   string
	router  *Router
	limiter *rateLimiter
	ring    *RingLog
	debug   bool
	startAt time.Time

	fcmMu          sync.Mutex
	fcmToken       string
	fcmTokenSource oauth2.TokenSource // nil when Firebase is not configured
}

// NewServer creates a new Server with the given bearer token and router.
func NewServer(token string, router *Router, ring *RingLog, debug bool, fcmTS oauth2.TokenSource) *Server {
	return &Server{
		token:          token,
		router:         router,
		limiter:        newRateLimiter(30, time.Minute),
		ring:           ring,
		debug:          debug,
		startAt:        time.Now(),
		fcmTokenSource: fcmTS,
	}
}

// Mux returns the HTTP handler mux.
func (s *Server) Mux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/share", s.handleShare)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/logs", s.handleLogs)
	mux.HandleFunc("/logs/stream", s.handleLogStream)
	mux.HandleFunc("/actions", s.handleActions)
	mux.HandleFunc("/register", s.handleRegister)
	mux.HandleFunc("/notify", s.handleNotify)
	return mux
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Auth — same bearer token as /share
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != s.token {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, line := range s.ring.Lines() {
		io.WriteString(w, line)
	}
}

func (s *Server) handleActions(w http.ResponseWriter, r *http.Request) {
	if s.debug {
		log.Printf("[DEBUG] %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	}

	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Auth — same bearer token as /share
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != s.token {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	actions := s.router.Actions()
	if s.debug {
		log.Printf("[DEBUG] returning %d actions", len(actions))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(actions)
}

func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Auth via query param (?token=...) since browsers can't set headers on EventSource.
	token := r.URL.Query().Get("token")
	if token == "" {
		// Also accept Authorization header for curl usage.
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(auth, "Bearer ") {
			token = strings.TrimPrefix(auth, "Bearer ")
		}
	}
	if token != s.token {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Replay buffered lines first.
	for _, line := range s.ring.Lines() {
		fmt.Fprintf(w, "data: %s\n", strings.TrimRight(line, "\n"))
	}
	flusher.Flush()

	// Stream new lines until client disconnects.
	ch := s.ring.Subscribe()
	defer s.ring.Unsubscribe(ch)

	for {
		select {
		case <-r.Context().Done():
			return
		case line := <-ch:
			fmt.Fprintf(w, "data: %s\n", strings.TrimRight(line, "\n"))
			flusher.Flush()
		}
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if s.debug {
		log.Printf("[DEBUG] %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	uptime := time.Since(s.startAt).Truncate(time.Second).String()
	fcmToken := s.GetFCMToken()
	actions := s.router.Actions()

	health := map[string]interface{}{
		"status":         "ok",
		"timestamp":      time.Now().UTC().Format(time.RFC3339),
		"uptime":         uptime,
		"actions":        len(actions),
		"fcm_enabled":    s.fcmTokenSource != nil,
		"fcm_registered": fcmToken != "",
		"debug":          s.debug,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health)
}

func (s *Server) handleShare(w http.ResponseWriter, r *http.Request) {
	if s.debug {
		log.Printf("[DEBUG] %s %s from %s content-length=%d", r.Method, r.URL.Path, r.RemoteAddr, r.ContentLength)
	}

	if r.Method != http.MethodPost {
		if s.debug {
			log.Printf("[DEBUG] rejected: method %s not allowed", r.Method)
		}
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Auth
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != s.token {
		if s.debug {
			log.Printf("[DEBUG] rejected: auth failed (has_header=%t)", auth != "")
		}
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Rate limit by remote IP
	ip := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		ip = strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	if !s.limiter.allow(ip) {
		if s.debug {
			log.Printf("[DEBUG] rejected: rate limit exceeded for ip=%s", ip)
		}
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}
	if s.debug {
		log.Printf("[DEBUG] auth ok, rate limit ok (ip=%s)", ip)
	}

	// Parse
	r.Body = http.MaxBytesReader(w, r.Body, maxPayloadSize)
	var req ShareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if s.debug {
			log.Printf("[DEBUG] rejected: JSON decode error: %v", err)
		}
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}
	if s.debug {
		log.Printf("[DEBUG] parsed: type=%q action=%q target=%q enter=%t text_len=%d url_len=%d", req.Type, req.Action, req.Target, req.Enter, len(req.Text), len(req.URL))
	}

	// Validate
	if err := validateRequest(&req); err != nil {
		if s.debug {
			log.Printf("[DEBUG] rejected: validation error: %v", err)
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Route
	result, err := s.router.Route(&req)
	if err != nil {
		log.Printf("error routing %s request: %v", req.Type, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	log.Printf("handled %s request → %s", req.Type, result)
	writeJSON(w, http.StatusOK, ShareResponse{
		Status:    "ok",
		Message:   result,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}

// registerRequest is the JSON body for POST /register.
type registerRequest struct {
	FCMToken string `json:"fcm_token"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if s.debug {
		log.Printf("[DEBUG] %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	}

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Auth — same bearer token as other endpoints.
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != s.token {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxPayloadSize)
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if req.FCMToken == "" {
		writeError(w, http.StatusBadRequest, "fcm_token is required")
		return
	}

	s.fcmMu.Lock()
	s.fcmToken = req.FCMToken
	s.fcmMu.Unlock()

	if s.debug {
		log.Printf("[DEBUG] FCM token registered (len=%d)", len(req.FCMToken))
	}
	log.Printf("FCM token registered")

	writeJSON(w, http.StatusOK, ShareResponse{
		Status:    "ok",
		Message:   "token registered",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}

// GetFCMToken returns the currently registered FCM device token.
func (s *Server) GetFCMToken() string {
	s.fcmMu.Lock()
	defer s.fcmMu.Unlock()
	return s.fcmToken
}

// notifyRequest is the JSON body for POST /notify.
type notifyRequest struct {
	Score int    `json:"score"`
	URL   string `json:"url"`
	Slug  string `json:"slug"`
}

func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	if s.debug {
		log.Printf("[DEBUG] %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	}

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Auth
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != s.token {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxPayloadSize)
	var req notifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	// Always log the notification.
	log.Printf("notify: score=%d url=%s slug=%s", req.Score, req.URL, req.Slug)

	if req.Score < 80 {
		if s.debug {
			log.Printf("[DEBUG] score %d < 80, skipping FCM push", req.Score)
		}
		writeJSON(w, http.StatusOK, ShareResponse{
			Status:    "ok",
			Message:   fmt.Sprintf("score %d below threshold, logged only", req.Score),
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	// Score >= 80 — attempt FCM push.
	deviceToken := s.GetFCMToken()
	if deviceToken == "" {
		log.Printf("WARN: score %d qualifies for push but no FCM token registered", req.Score)
		writeJSON(w, http.StatusOK, ShareResponse{
			Status:    "ok",
			Message:   "no FCM token registered, logged only",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	if s.fcmTokenSource == nil {
		log.Printf("WARN: score %d qualifies for push but Firebase SA not configured", req.Score)
		writeJSON(w, http.StatusOK, ShareResponse{
			Status:    "ok",
			Message:   "firebase not configured, logged only",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	if err := s.sendFCMPush(deviceToken, req.Score, req.Slug); err != nil {
		log.Printf("ERROR: FCM push failed: %v", err)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("FCM push failed: %v", err))
		return
	}

	log.Printf("FCM push sent: score=%d slug=%s", req.Score, req.Slug)
	writeJSON(w, http.StatusOK, ShareResponse{
		Status:    "ok",
		Message:   fmt.Sprintf("push sent (score=%d)", req.Score),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}

// fcmEndpoint is the FCM HTTP v1 API endpoint.
const fcmEndpoint = "https://fcm.googleapis.com/v1/projects/LINKARI_FCM_PROJECT_ID/messages:send"

// sendFCMPush sends a push notification via the FCM HTTP v1 API.
func (s *Server) sendFCMPush(deviceToken string, score int, slug string) error {
	tok, err := s.fcmTokenSource.Token()
	if err != nil {
		return fmt.Errorf("obtaining oauth2 token: %w", err)
	}

	payload := map[string]interface{}{
		"message": map[string]interface{}{
			"token": deviceToken,
			"notification": map[string]string{
				"title": fmt.Sprintf("uinit: %d/100", score),
				"body":  slug,
			},
			"android": map[string]string{
				"priority": "high",
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling FCM payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, fcmEndpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating FCM request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending FCM request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("FCM returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

func validateRequest(req *ShareRequest) error {
	switch req.Type {
	case "text":
		if req.Text == "" {
			return fmt.Errorf("text field required for type=text")
		}
		if len(req.Text) > 4096 {
			return fmt.Errorf("text exceeds 4096 character limit")
		}
	case "url":
		if req.URL == "" {
			return fmt.Errorf("url field required for type=url")
		}
		if len(req.URL) > 2048 {
			return fmt.Errorf("url exceeds 2048 character limit")
		}
		if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
			return fmt.Errorf("url must start with http:// or https://")
		}
	default:
		return fmt.Errorf("unsupported type %q (expected text or url)", req.Type)
	}
	return nil
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, ShareResponse{
		Status:    "error",
		Message:   msg,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// rateLimiter implements a simple sliding window rate limiter.
type rateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	clients map[string][]time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		limit:   limit,
		window:  window,
		clients: make(map[string][]time.Time),
	}
}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	// Prune expired entries
	times := rl.clients[key]
	start := 0
	for start < len(times) && times[start].Before(cutoff) {
		start++
	}
	times = times[start:]

	if len(times) >= rl.limit {
		rl.clients[key] = times
		return false
	}

	rl.clients[key] = append(times, now)
	return true
}
