package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// maxPayloadSize limits request body to 64KB.
const maxPayloadSize = 64 * 1024

// registerFaultEnv is the env var that, when set to a 5xx status code,
// causes POST /register to short-circuit with that status before touching
// the devices table. Debug-only; used by linkari-android FcmRegisterWorker
// to exercise its Result.retry() 5xx backoff branch (EPIC-045 M6 Check 4).
const registerFaultEnv = "LINKARI_REGISTER_FAULT"

// ValidateRegisterFaultEnv parses $LINKARI_REGISTER_FAULT at startup.
// Returns 0 when unset; a value in [500,599] when valid; fatals otherwise.
// 2xx/4xx codes are rejected so operators cannot accidentally mask real
// registration failures with a 200/400 short-circuit.
func ValidateRegisterFaultEnv() int {
	v := strings.TrimSpace(os.Getenv(registerFaultEnv))
	if v == "" {
		return 0
	}
	code, err := strconv.Atoi(v)
	if err != nil {
		log.Fatalf("FATAL: %s=%q is not an integer", registerFaultEnv, v)
	}
	if code < 500 || code > 599 {
		log.Fatalf("FATAL: %s=%d must be in [500,599] (2xx/4xx rejected)", registerFaultEnv, code)
	}
	return code
}

// ShareRequest is the incoming payload from Android HTTP Shortcuts.
type ShareRequest struct {
	Type     string `json:"type"`
	Action   string `json:"action,omitempty"`
	Text     string `json:"text,omitempty"`
	URL      string `json:"url,omitempty"`
	Title    string `json:"title,omitempty"`
	Target   string `json:"target,omitempty"`
	Enter    bool   `json:"enter"`
	Profile  string `json:"profile,omitempty"`
	FCMToken string `json:"fcm_token,omitempty"`
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
	queue   *Queue
	limiter *rateLimiter
	ring    *RingLog
	events  *EventLogger // nil when event logging is not configured
	debug   bool
	startAt time.Time
	tsnetAddr string // Funnel FQDN; empty when tsnet is not enabled

	fcmMu          sync.Mutex
	fcmToken       string
	fcmTokenSource oauth2.TokenSource // nil when Firebase is not configured

	notifyMinScore int // configurable floor for FCM push in /notify; 0 = use per-profile archiveThreshold
	lastDigestPush time.Time
}

// NewServer creates a new Server with the given bearer token, router, and optional queue.
func NewServer(token string, router *Router, queue *Queue, ring *RingLog, debug bool, fcmTS oauth2.TokenSource) *Server {
	return &Server{
		token:          token,
		router:         router,
		queue:          queue,
		limiter:        newRateLimiter(30, time.Minute),
		ring:           ring,
		debug:          debug,
		startAt:        time.Now(),
		fcmTokenSource: fcmTS,
	}
}

// SetTsnetAddr records the tsnet Funnel address for health reporting.
func (s *Server) SetTsnetAddr(addr string) {
	s.tsnetAddr = addr
}

// corsMiddleware adds CORS headers to all responses and handles preflight OPTIONS requests.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Mux returns the full HTTP handler mux (for local listener).
func (s *Server) Mux() http.Handler {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/logs", s.handleLogs)
	mux.HandleFunc("/logs/stream", s.handleLogStream)
	return corsMiddleware(mux)
}

// FunnelMux returns a restricted mux for the public Funnel listener.
// Local-only endpoints (/healthz, /logs, /logs/stream) are excluded.
func (s *Server) FunnelMux() http.Handler {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	return corsMiddleware(mux)
}

// registerRoutes adds the shared authenticated routes to a mux.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/share", s.handleShare)
	mux.HandleFunc("/actions", s.handleActions)
	mux.HandleFunc("/register", s.handleRegister)
	mux.HandleFunc("/notify", s.handleNotify)
	mux.HandleFunc("/queue", s.handleQueue)
	mux.HandleFunc("POST /queue/{id}/score", s.handleQueueScore)
	mux.HandleFunc("/archive", s.handleArchive)
	mux.HandleFunc("/digest", s.handleDigest)
	mux.HandleFunc("POST /search", s.handleSearch)
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
		"tsnet_enabled":  s.tsnetAddr != "",
		"tsnet_addr":     s.tsnetAddr,
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
		log.Printf("[DEBUG] parsed: type=%q action=%q profile=%q target=%q enter=%t text_len=%d url_len=%d title=%q", req.Type, req.Action, req.Profile, req.Target, req.Enter, len(req.Text), len(req.URL), req.Title)
	}

	shareStart := time.Now()

	// Validate
	if err := validateRequest(&req); err != nil {
		if s.debug {
			log.Printf("[DEBUG] rejected: validation error: %v", err)
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Enqueue for persistence (before routing — survives tmux failures).
	var queueID int64
	if s.queue != nil {
		id, err := s.queue.Enqueue(&req)
		if err != nil {
			log.Printf("WARN: queue enqueue failed: %v", err)
		} else {
			queueID = id
			if s.debug {
				log.Printf("[DEBUG] queue: enqueued id=%d", id)
			}
		}
	}

	// Route
	result, err := s.router.Route(&req)
	if err != nil {
		s.emitShareEvent(&req, "failure", shareStart, "")
		// If queue is active, return 200 "queued" instead of 500 —
		// the replay goroutine will retry when tmux is available.
		if s.queue != nil {
			log.Printf("queued %s request (profile=%s): routing failed: %v", req.Type, req.Profile, err)
			writeJSON(w, http.StatusOK, ShareResponse{
				Status:    "queued",
				Message:   "tmux unavailable, queued for replay",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			})
			return
		}
		log.Printf("error routing %s request: %v", req.Type, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Mark as relayed immediately since routing succeeded.
	// Uses the exact enqueued ID to prevent replay goroutine from re-processing.
	if s.queue != nil && queueID > 0 {
		s.queue.MarkRelayed(queueID)
	}

	s.emitShareEvent(&req, "success", shareStart, req.URL)

	log.Printf("handled %s request (profile=%s title=%q) → %s", req.Type, req.Profile, req.Title, result)
	writeJSON(w, http.StatusOK, ShareResponse{
		Status:    "ok",
		Message:   result,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != s.token {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if s.queue == nil {
		writeError(w, http.StatusServiceUnavailable, "queue not configured")
		return
	}

	status := r.URL.Query().Get("status")
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}

	items, err := s.queue.List(status, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("queue list: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

type scoreRequest struct {
	Score   int    `json:"score"`
	Slug    string `json:"slug"`
	Tags    string `json:"tags"`
	Verdict string `json:"verdict"`
}

func (s *Server) handleQueueScore(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != s.token {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if s.queue == nil {
		writeError(w, http.StatusServiceUnavailable, "queue not configured")
		return
	}

	// Extract ID from path: /queue/{id}/score
	idStr := r.PathValue("id")
	var id int64
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid queue item ID")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxPayloadSize)
	var req scoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if err := s.queue.UpdateScore(id, req.Score, req.Tags, req.Verdict, req.Slug); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("update score: %v", err))
		return
	}

	// Auto-archive if score meets profile threshold.
	item, err := s.queue.GetByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("get item: %v", err))
		return
	}

	threshold := archiveThreshold(item.Profile)
	if threshold >= 0 && req.Score >= threshold {
		s.queue.Archive(id)
		item.Status = "archived"
		log.Printf("archive: id=%d score=%d profile=%s tags=%s", id, req.Score, item.Profile, req.Tags)

		// FCM digest push — at most once per hour.
		s.maybeDigestPush(req.Score, req.Slug)
	} else {
		log.Printf("scored: id=%d score=%d profile=%s (threshold=%d)", id, req.Score, item.Profile, threshold)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(item)
}

func (s *Server) maybeDigestPush(score int, slug string) {
	if time.Since(s.lastDigestPush) < time.Hour {
		return
	}
	if s.queue == nil {
		return
	}
	id, err := s.queue.EnqueuePush("digest", score, slug, "", "")
	if err != nil {
		log.Printf("WARN: digest enqueue failed: %v", err)
		return
	}
	s.lastDigestPush = time.Now()
	emitPushEvent("push_outbox_enqueued", map[string]interface{}{
		"id": id, "kind": "digest", "score": score, "slug": slug,
	})
	if s.debug {
		log.Printf("[DEBUG] digest push enqueued id=%d score=%d slug=%s", id, score, slug)
	}
}

func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != s.token {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.queue == nil {
		writeError(w, http.StatusServiceUnavailable, "queue not configured")
		return
	}

	profile := r.URL.Query().Get("profile")
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}

	items, err := s.queue.ListArchived(profile, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("list archived: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func (s *Server) handleDigest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != s.token {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.queue == nil {
		writeError(w, http.StatusServiceUnavailable, "queue not configured")
		return
	}

	digestStart := time.Now()

	since := time.Now().Add(-24 * time.Hour)
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}

	items, err := s.queue.RecentScored(since, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("recent scored: %v", err))
		return
	}

	// Determine dominant profile from digest items.
	profile := r.URL.Query().Get("profile")
	s.emitDigestEvent(profile, len(items), digestStart)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != s.token {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.queue == nil {
		writeError(w, http.StatusServiceUnavailable, "queue not configured")
		return
	}

	var req struct {
		Query   string `json:"query"`
		Profile string `json:"profile"`
		Limit   int    `json:"limit"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPayloadSize)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}

	items, err := s.queue.SearchFTS5(req.Query, req.Profile, req.Limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("search: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
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

	// EPIC-045 M6 Check 4: debug-only fault injection for android
	// FcmRegisterWorker retry path. Short-circuits BEFORE touching the
	// devices table so replaying the toggle does not corrupt state.
	if code := ValidateRegisterFaultEnv(); code != 0 {
		emitPushEvent("push_register_fault_injected", map[string]interface{}{
			"status": code,
		})
		w.WriteHeader(code)
		return
	}

	s.fcmMu.Lock()
	s.fcmToken = req.FCMToken
	s.fcmMu.Unlock()

	// EPIC-045 M3: durably upsert into devices table.
	if s.queue != nil {
		if err := s.queue.UpsertDevice(req.FCMToken); err != nil {
			log.Printf("WARN: device upsert failed: %v", err)
		} else {
			emitPushEvent("push_register_upsert", map[string]interface{}{
				"token_len": len(req.FCMToken),
			})
		}
	}

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
// When called from the _score.json sidecar callback, all fields are populated.
type notifyRequest struct {
	Score       int    `json:"score"`
	URL         string `json:"url"`
	Slug        string `json:"slug"`
	Profile     string `json:"profile,omitempty"`
	Verdict     string `json:"verdict,omitempty"`
	Tags        string `json:"tags,omitempty"`
	ActionItems string `json:"action_items,omitempty"`
	ScoredAt    string `json:"scored_at,omitempty"`
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
	log.Printf("notify: score=%d profile=%s url=%s slug=%s verdict_len=%d", req.Score, req.Profile, req.URL, req.Slug, len(req.Verdict))

	// Persist score + verdict to queue and auto-archive if threshold met.
	if s.queue != nil && req.URL != "" {
		item, _, err := s.queue.ScoreByURL(req.URL, req.Score, req.Verdict, req.Tags, req.Profile, req.Slug)
		if err != nil {
			log.Printf("WARN: notify queue persist: %v", err)
		} else {
			at := archiveThreshold(req.Profile)
			if at >= 0 && item.Score != nil && *item.Score >= at {
				if archErr := s.queue.Archive(item.ID); archErr == nil {
					if s.debug {
						log.Printf("[DEBUG] auto-archived item %d (score=%d threshold=%d)", item.ID, *item.Score, at)
					}
				}
			}
		}
	}

	threshold := archiveThreshold(req.Profile)
	if s.notifyMinScore > 0 {
		threshold = s.notifyMinScore
	}
	if threshold < 0 || req.Score < threshold {
		if s.debug {
			log.Printf("[DEBUG] score %d below threshold %d (profile=%s), skipping FCM push", req.Score, threshold, req.Profile)
		}
		writeJSON(w, http.StatusOK, ShareResponse{
			Status:    "ok",
			Message:   fmt.Sprintf("score %d below threshold %d, logged only", req.Score, threshold),
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	if s.queue == nil {
		writeError(w, http.StatusServiceUnavailable, "queue not configured")
		return
	}

	id, err := s.queue.EnqueuePush("notify", req.Score, req.Slug, req.Verdict, req.URL)
	if err != nil {
		log.Printf("ERROR: notify enqueue failed: %v", err)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("enqueue: %v", err))
		return
	}
	emitPushEvent("push_outbox_enqueued", map[string]interface{}{
		"id": id, "kind": "notify", "score": req.Score, "slug": req.Slug, "url": req.URL,
	})
	log.Printf("push enqueued: id=%d score=%d slug=%s", id, req.Score, req.Slug)
	writeJSON(w, http.StatusOK, ShareResponse{
		Status:    "ok",
		Message:   fmt.Sprintf("push enqueued id=%d (score=%d)", id, req.Score),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}

// fcmEndpoint is the FCM HTTP v1 API endpoint (used by the push outbox worker).
const fcmEndpoint = "https://fcm.googleapis.com/v1/projects/LINKARI_FCM_PROJECT_ID/messages:send"

// firstSentence extracts the first sentence from text, truncating to maxLen.
// It splits on ". ", "— ", or newline boundaries to find a natural break.
func firstSentence(text string, maxLen int) string {
	// Try natural sentence boundaries.
	for _, sep := range []string{". ", " — ", "\n"} {
		if idx := strings.Index(text, sep); idx > 0 && idx <= maxLen {
			return text[:idx+1]
		}
	}
	// No boundary found within limit — hard truncate.
	if len(text) > maxLen {
		// Try to break at last space.
		if sp := strings.LastIndex(text[:maxLen], " "); sp > maxLen/2 {
			return text[:sp] + "…"
		}
		return text[:maxLen-1] + "…"
	}
	return text
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

// emitShareEvent logs a linkari_share JSONL event.
func (s *Server) emitShareEvent(req *ShareRequest, status string, start time.Time, rawURL string) {
	if s.events == nil {
		return
	}
	meta := map[string]interface{}{
		"profile":     req.Profile,
		"url_domain":  domainFromURL(rawURL),
		"status":      status,
		"duration_ms": time.Since(start).Milliseconds(),
	}
	if err := s.events.Emit("linkari_share", meta); err != nil {
		log.Printf("WARN: event emit linkari_share: %v", err)
	}
}

// emitDigestEvent logs a linkari_digest JSONL event.
func (s *Server) emitDigestEvent(profile string, itemCount int, start time.Time) {
	if s.events == nil {
		return
	}
	meta := map[string]interface{}{
		"profile":     profile,
		"item_count":  itemCount,
		"duration_ms": time.Since(start).Milliseconds(),
	}
	if err := s.events.Emit("linkari_digest", meta); err != nil {
		log.Printf("WARN: event emit linkari_digest: %v", err)
	}
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
