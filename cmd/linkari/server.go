package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/blo-grindr/runabout/cmd/linkari/internal/linklog"
)

// maxPayloadSize limits request body to 64KB.
const maxPayloadSize = 64 * 1024

// maxAudioSize limits multipart audio uploads to 200MB.
const maxAudioSize = 200 << 20

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
		slog.Error("register fault env is not an integer", "var", registerFaultEnv, "value", v)
		os.Exit(1)
	}
	if code < 500 || code > 599 {
		slog.Error("register fault env must be in [500,599]", "var", registerFaultEnv, "value", code)
		os.Exit(1)
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

	// EPIC-038 M3: Intent metadata fields from Android share intent.
	MimeType       string `json:"mime_type,omitempty"`
	CallingPackage string `json:"calling_package,omitempty"`
	AppCategory    int    `json:"app_category,omitempty"`
	IsScreenshot   bool   `json:"is_screenshot,omitempty"`
	ExtraSubject   string `json:"extra_subject,omitempty"`
	ExtraText      string `json:"extra_text,omitempty"`
	// FileSize is the file size in bytes from MediaStore. Telemetry-only —
	// not used in classification or scoring decisions. Retained for future
	// use (e.g., early rejection of oversized non-audio document shares).
	FileSize int64 `json:"file_size,omitempty"`
	RelativePath   string `json:"relative_path,omitempty"`
	Filename       string `json:"filename,omitempty"`

	// Internal fields — not serialized from JSON.
	AudioPath        string `json:"-"` // EPIC-067: temp file path for uploaded audio
	QueueRowID       int64  `json:"-"` // EPIC-067: queue row ID for audio scoring (no URL to match on)
	OriginalFilename string `json:"-"` // EPIC-071: original filename from multipart upload
	ClassifySource       string `json:"-"` // EPIC-077 M1: cascade stage that won pre-enqueue classification
	ForceContentClassify bool   `json:"-"` // EPIC-085 M2: per-action flag to always run content-LLM reclassification
}

// ShareResponse is the structured JSON response.
// EPIC-055 U1: id and slug are included so the Android client can poll
// /archive?status=scored for the scored row without a separate lookup.
//
// EPIC-077 M1: ClassifySource reflects the pre-enqueue synchronous cascade
// stage that determined the profile (e.g. "intent_metadata", "url_domain",
// "caller"). Always populated — the fast cascade runs synchronously before
// Enqueue. The async Haiku content classification in scoreURLAsync may
// override the profile after this response is sent; the final classify_source
// is surfaced via /queue/{id} (EPIC-077 M6).
type ShareResponse struct {
	Status         string `json:"status"`
	Message        string `json:"message"`
	Timestamp      string `json:"timestamp"`
	ID             int64  `json:"id,omitempty"`              // queue row id for client correlation (U1)
	Slug           string `json:"slug,omitempty"`            // URL slug for /archive polling (U1)
	ClassifySource string `json:"classify_source,omitempty"` // EPIC-076: pre-goroutine routing signal; absent on async ServerScore path
	Duplicate      bool   `json:"duplicate,omitempty"`       // EPIC-078 M5: true when a recent identical file share was found
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
	token     string
	jiraToken      string // EPIC-057: scoped bearer for ginit_* actions; empty = Jira ingress disabled
	jiraAPIUsername string // outbound Jira API username (from linkari/jira-webhook secret)
	jiraAPIPassword string // outbound Jira API password
	jiraDomain      string // e.g. "xxx.atlassian.net"
	pagerDutyToken  string // PagerDuty API token
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

	// EPIC-052: share action resolution policy. Default false (caller-wins).
	// Populated from ServerConfig.Share.HeuristicOverrideEnabled at boot.
	shareHeuristicOverride bool

	// EPIC-073: shield middleware for funnel client identity enforcement.
	shield *Shield // nil when shield is not configured

	// EPIC-001: Google Sign-In support.
	googleVerifier *GoogleTokenVerifier // nil when Google Sign-In is not configured
	sessionTTLDays int                  // session token TTL; 0 = use default (90 days)
	// EPIC-051 M3: lastDigestPush deleted — throttle state lives in SQL via
	// Queue.EnqueueDigestIfDue. Do not re-add in-memory throttle state here.

	// EPIC-001 M3: IP blocklist and CORS origins.
	blocklist   []*net.IPNet // parsed CIDRs (single IPs stored as /32 or /128)
	corsOrigins []string     // allowed CORS origins for Funnel; empty = wildcard

	// GAP-08: metrics collection. nil when metrics.enabled=false in server.yaml.
	metrics *MetricsCollector
}

// NewServer creates a new Server with the given bearer token, router, and optional queue.
func NewServer(token string, router *Router, queue *Queue, ring *RingLog, debug bool, fcmTS oauth2.TokenSource) *Server {
	if router != nil && queue != nil {
		router.SetQueue(queue)
	}
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

// SetShield installs (or replaces) the shield middleware on the server.
// NewServer signature is frozen (G-15); use this setter instead.
func (s *Server) SetShield(shield *Shield) {
	s.shield = shield
}

// SetMetrics installs the MetricsCollector. Called from main.go after
// NewMetricsCollector; no-op when m is nil (metrics.enabled=false).
func (s *Server) SetMetrics(m *MetricsCollector) {
	s.metrics = m
}

// SetTsnetAddr records the tsnet Funnel address for health reporting.
func (s *Server) SetTsnetAddr(addr string) {
	s.tsnetAddr = addr
}

// SetBlocklist parses IP/CIDR strings and stores them for blocklist middleware.
func (s *Server) SetBlocklist(entries []string) {
	for _, entry := range entries {
		_, cidr, err := net.ParseCIDR(entry)
		if err != nil {
			// Try as bare IP.
			ip := net.ParseIP(entry)
			if ip == nil {
				slog.Warn("blocklist: skipping invalid entry", "entry", entry)
				continue
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			cidr = &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}
		}
		s.blocklist = append(s.blocklist, cidr)
	}
	if len(s.blocklist) > 0 {
		slog.Info("blocklist loaded", "entries", len(s.blocklist))
	}
}

// SetCORSOrigins sets the allowed CORS origins for FunnelMux.
func (s *Server) SetCORSOrigins(origins []string) {
	s.corsOrigins = origins
}

// blocklistMiddleware rejects requests from blocked IPs with 403.
func (s *Server) blocklistMiddleware(next http.Handler) http.Handler {
	if len(s.blocklist) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := net.ParseIP(realIPFromContext(r.Context(), r.RemoteAddr))
		if ip != nil {
			for _, cidr := range s.blocklist {
				if cidr.Contains(ip) {
					http.Error(w, "Forbidden", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// authenticateRequest checks if the request carries a valid bearer token.
// Accepts the operator token (s.token) OR a valid session token (EPIC-001).
// Returns true if authenticated. Does NOT check action-scoped restrictions
// (use checkScopedAuth for that).
func (s *Server) authenticateRequest(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	bearer := strings.TrimPrefix(auth, "Bearer ")
	if bearer == s.token {
		return true
	}
	_, ok := s.checkSessionAuth(bearer)
	return ok
}

// funnelCORSMiddleware restricts CORS to configured origins on the Funnel
// listener. When s.corsOrigins is empty, falls back to wildcard "*".
func (s *Server) funnelCORSMiddleware(next http.Handler) http.Handler {
	if len(s.corsOrigins) == 0 {
		return corsMiddleware(next)
	}
	allowed := make(map[string]bool, len(s.corsOrigins))
	for _, o := range s.corsOrigins {
		allowed[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowed[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Linkari-Client")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware adds CORS headers to all responses and handles preflight OPTIONS requests.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Linkari-Client")
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
	return traceMiddleware(corsMiddleware(mux))
}

// FunnelMux returns a restricted mux for the public Funnel listener.
// Only explicitly allowlisted routes are registered — everything else
// returns 404 to scanners. Local-only endpoints (/healthz, /logs,
// /logs/stream, /notify) are excluded.
// Middleware chain: traceMiddleware → funnelCORS → blocklist → funnelAuthGuard → shieldMiddleware → mux
func (s *Server) FunnelMux() http.Handler {
	mux := http.NewServeMux()
	s.registerFunnelRoutes(mux)
	var handler http.Handler = mux
	handler = s.funnelAuthGuardMiddleware(handler)
	if s.shield != nil {
		handler = s.shield.Middleware(handler)
	}
	handler = s.blocklistMiddleware(handler)
	return traceMiddleware(s.funnelCORSMiddleware(handler))
}

// statusRecorder wraps http.ResponseWriter to capture the status code for
// request logging. Flush/Hijack/Push are intentionally not implemented —
// /logs/stream (the only SSE endpoint) writes its headers explicitly and
// does not require a hijackable wrapper; the default 200 default is fine.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (sr *statusRecorder) WriteHeader(code int) {
	if !sr.wrote {
		sr.status = code
		sr.wrote = true
	}
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if !sr.wrote {
		sr.status = http.StatusOK
		sr.wrote = true
	}
	return sr.ResponseWriter.Write(b)
}

// Flush forwards to the underlying ResponseWriter if it supports
// http.Flusher. /logs/stream relies on this for SSE.
func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// traceMiddleware mints a trace_id, attaches it to r.Context() via
// linklog.WithTraceID, and emits a structured request log line with
// method, path, status, duration_ms, and event_type=http_request on
// completion. Downstream handlers access the trace_id via
// linklog.TraceIDFromContext(r.Context()) for correlation.
func traceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := uuid.NewString()
		ctx := linklog.WithTraceID(r.Context(), traceID)
		r = r.WithContext(ctx)

		// Advertise the trace ID on the response so clients can correlate.
		w.Header().Set("X-Trace-ID", traceID)

		sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sr, r)
		dur := time.Since(start)

		slog.InfoContext(ctx, "http_request",
			"event_type", "http_request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sr.status,
			"duration_ms", dur.Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

// registerRoutes adds the shared authenticated routes to a mux.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/share", s.handleShare)
	mux.HandleFunc("/actions", s.handleActions)
	mux.HandleFunc("/register", s.handleRegister)
	mux.HandleFunc("/notify", s.handleNotify)
	mux.HandleFunc("POST /push/test", s.handleTestPush)
	mux.HandleFunc("/queue", s.handleQueue)
	mux.HandleFunc("POST /queue/{id}/score", s.handleQueueScore)
	mux.HandleFunc("POST /queue/{id}/outcome", s.handleQueueOutcome)
	mux.HandleFunc("POST /queue/{id}/feedback", s.handleQueueFeedback)
	mux.HandleFunc("POST /queue/slug/{slug}/feedback", s.handleQueueFeedbackBySlug)
	mux.HandleFunc("POST /queue/slug/{slug}/outcome", s.handleQueueOutcomeBySlug)
	mux.HandleFunc("GET /profiles/stats", s.handleProfileStats)
	mux.HandleFunc("/archive", s.handleArchive)
	mux.HandleFunc("/digest", s.handleDigest)
	mux.HandleFunc("POST /search", s.handleSearch)
	// EPIC-001: auth endpoints.
	mux.HandleFunc("POST /auth/google", s.handleAuthGoogle)
	mux.HandleFunc("POST /auth/invite", s.handleAuthInvite)
	mux.HandleFunc("POST /admin/invite", s.handleAdminInvite)
}

// registerFunnelRoutes adds the public-facing route allowlist for the Funnel
// listener. Only endpoints that external clients legitimately need are
// registered — everything else (e.g. /notify, /admin/invite, /push/test)
// is excluded so scanners get 404.
func (s *Server) registerFunnelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/share", s.handleShare)
	mux.HandleFunc("/actions", s.handleActions)
	mux.HandleFunc("/register", s.handleRegister)
	mux.HandleFunc("/queue", s.handleQueue)
	mux.HandleFunc("POST /queue/{id}/score", s.handleQueueScore)
	mux.HandleFunc("POST /queue/{id}/outcome", s.handleQueueOutcome)
	mux.HandleFunc("POST /queue/{id}/feedback", s.handleQueueFeedback)
	mux.HandleFunc("POST /queue/slug/{slug}/feedback", s.handleQueueFeedbackBySlug)
	mux.HandleFunc("POST /queue/slug/{slug}/outcome", s.handleQueueOutcomeBySlug)
	mux.HandleFunc("GET /profiles/stats", s.handleProfileStats)
	mux.HandleFunc("/archive", s.handleArchive)
	mux.HandleFunc("/digest", s.handleDigest)
	mux.HandleFunc("POST /search", s.handleSearch)
	mux.HandleFunc("POST /auth/google", s.handleAuthGoogle)
	mux.HandleFunc("POST /auth/invite", s.handleAuthInvite)
	mux.HandleFunc("GET /health", s.handleHealthMinimal)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Auth — operator token or session token (EPIC-001).
	if !s.authenticateRequest(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, line := range s.ring.Lines() {
		io.WriteString(w, line)
	}
}

func (s *Server) handleActions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Auth — operator token or session token (EPIC-001).
	if !s.authenticateRequest(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	actions := s.router.Actions()
	slog.DebugContext(ctx, "returning actions", "count", len(actions))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(actions)
}

func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Auth via query param (?token=...) since browsers can't set headers on EventSource.
	// EPIC-001: also accepts session tokens via Authorization header.
	token := r.URL.Query().Get("token")
	if token == "" {
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(auth, "Bearer ") {
			token = strings.TrimPrefix(auth, "Bearer ")
		}
	}
	if token != s.token {
		if _, ok := s.checkSessionAuth(token); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
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

// handleHealthMinimal is the Funnel-safe health probe — returns only
// {"status":"ok"} with no internal state. Registered on FunnelMux as /health.
func (s *Server) handleHealthMinimal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}` + "\n"))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authenticateRequest(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	uptime := time.Since(s.startAt).Truncate(time.Second).String()
	fcmToken := s.GetFCMToken()
	actions := s.router.Actions()

	// Probe the DB so corruption or mid-session failures surface here
	// rather than as 500s on /archive or /digest (2026-04-13 incident).
	dbStatus := "ok"
	var dbError string
	if s.queue != nil {
		if err := s.queue.Ping(); err != nil {
			dbStatus = "error"
			dbError = err.Error()
		}
	}

	status := "ok"
	code := http.StatusOK
	if dbStatus != "ok" {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}

	health := map[string]interface{}{
		"status":         status,
		"timestamp":      time.Now().UTC().Format(time.RFC3339),
		"uptime":         uptime,
		"actions":        len(actions),
		"fcm_enabled":    s.fcmTokenSource != nil,
		"fcm_registered": fcmToken != "",
		"tsnet_enabled":  s.tsnetAddr != "",
		"tsnet_addr":     s.tsnetAddr,
		"debug":          s.debug,
		"db":             dbStatus,
	}
	if dbError != "" {
		health["db_error"] = dbError
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(health)
}

// jiraKeyRegex validates Jira issue keys at the HTTP boundary. Only keys
// matching this pattern may reach tmux send-keys via ginit_* templates.
//
// Jira Ingress Invariant (EPIC-057): No Jira-controlled byte may reach
// `tmux send-keys -l` except via jiraKeyRegex-validated req.Text. The ginit_*
// command template uses only {{.Text}} (never {{.Title}} or {{.URL}}). The
// scoped-auth helper ensures requests bearing jira_token can only invoke
// ginit_* action IDs, and requests bearing the mobile LINKARI_TOKEN cannot
// invoke ginit_* actions. This invariant sits alongside the caller-wins
// invariant (EPIC-052) and dual-writer invariant (EPIC-051).
var jiraKeyRegex = regexp.MustCompile(`^[A-Z][A-Z0-9_]+-\d+$`)

// checkScopedAuth verifies that the bearer token is authorized for the resolved
// action. Returns (tokenKind, allowed). Token kinds: "mobile", "jira", "session", "unknown".
// EPIC-001: session tokens are accepted with the same restrictions as mobile
// (allowed for everything except ginit_*).
func (s *Server) checkScopedAuth(bearer, actionID string) (kind string, allowed bool) {
	isGinit := strings.HasPrefix(actionID, "ginit_")
	switch {
	case bearer == s.token:
		// Mobile/Chrome token: allowed for everything except ginit_*.
		return "mobile", !isGinit
	case s.jiraToken != "" && bearer == s.jiraToken:
		// Jira token: allowed only for ginit_*.
		return "jira", isGinit
	default:
		// EPIC-001: check session token as fallback.
		if _, ok := s.checkSessionAuth(bearer); ok {
			return "session", !isGinit
		}
		return "unknown", false
	}
}

func (s *Server) handleShare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodPost {
		slog.DebugContext(ctx, "share rejected: method not allowed", "method", r.Method)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Auth: accept mobile/Chrome token, Jira-scoped token, or session token (EPIC-001).
	auth := r.Header.Get("Authorization")
	bearer := strings.TrimPrefix(auth, "Bearer ")
	if !strings.HasPrefix(auth, "Bearer ") {
		slog.DebugContext(ctx, "share rejected: auth failed", "has_header", auth != "")
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	isMobileOrJira := bearer == s.token || (s.jiraToken != "" && bearer == s.jiraToken)
	_, isSession := s.checkSessionAuth(bearer)
	if !isMobileOrJira && !isSession {
		slog.DebugContext(ctx, "share rejected: auth failed", "has_header", auth != "")
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Rate limit by real client IP — extracted from FunnelConn.Src on Funnel
	// connections, or RemoteAddr on local. Never trust X-Forwarded-For (GAP-5).
	ip := realIPFromContext(r.Context(), r.RemoteAddr)
	if !s.limiter.allow(ip) {
		slog.DebugContext(ctx, "share rejected: rate limit exceeded", "ip", ip)
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	// Parse — branch on Content-Type: multipart/form-data (EPIC-067 audio)
	// vs application/json (existing path).
	var req ShareRequest
	ct := r.Header.Get("Content-Type")
	mediaType, _, _ := mime.ParseMediaType(ct)

	if mediaType == "multipart/form-data" {
		// EPIC-067: streaming multipart — 200MB limit applied on r.Body so
		// the audio part streams directly to disk via io.Copy (~36KB RAM per
		// request instead of buffering the whole file in memory).
		r.Body = http.MaxBytesReader(w, r.Body, maxAudioSize)
		_, params, _ := mime.ParseMediaType(ct)
		boundary := params["boundary"]
		if boundary == "" {
			writeError(w, http.StatusBadRequest, "missing multipart boundary")
			return
		}
		mr := multipart.NewReader(r.Body, boundary)
		req.Type = "audio" // default; overridden below if mime_type is sent (EPIC-038)

		var audioWritten int64
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				slog.DebugContext(ctx, "share rejected: multipart read error", "error", err)
				writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid multipart: %v", err))
				return
			}
			switch part.FormName() {
			case "action":
				b, _ := io.ReadAll(io.LimitReader(part, 1024))
				req.Action = string(b)
			case "fcm_token":
				b, _ := io.ReadAll(io.LimitReader(part, 4096))
				req.FCMToken = string(b)
			case "profile":
				b, _ := io.ReadAll(io.LimitReader(part, 256))
				req.Profile = string(b)
			// EPIC-038 M3: intent metadata fields.
			case "mime_type":
				b, _ := io.ReadAll(io.LimitReader(part, 256))
				req.MimeType = string(b)
			case "calling_package":
				b, _ := io.ReadAll(io.LimitReader(part, 512))
				req.CallingPackage = string(b)
			case "app_category":
				b, _ := io.ReadAll(io.LimitReader(part, 16))
				if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
					req.AppCategory = n
				}
			case "is_screenshot":
				b, _ := io.ReadAll(io.LimitReader(part, 8))
				req.IsScreenshot = strings.TrimSpace(string(b)) == "true" || strings.TrimSpace(string(b)) == "1"
			case "extra_subject":
				b, _ := io.ReadAll(io.LimitReader(part, 4096))
				req.ExtraSubject = string(b)
			case "extra_text":
				b, _ := io.ReadAll(io.LimitReader(part, 4096))
				req.ExtraText = string(b)
			case "file_size":
				b, _ := io.ReadAll(io.LimitReader(part, 32))
				if n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
					req.FileSize = n
				}
			case "date_added":
				// EPIC-079 M5: DateAdded removed from ShareRequest — drain but ignore.
				io.Copy(io.Discard, part)
			case "relative_path":
				b, _ := io.ReadAll(io.LimitReader(part, 1024))
				req.RelativePath = string(b)
			case "filename":
				b, _ := io.ReadAll(io.LimitReader(part, 1024))
				req.Filename = string(b)
			case "type":
				b, _ := io.ReadAll(io.LimitReader(part, 64))
				req.Type = string(b)
			case "audio", "file":
				req.OriginalFilename = part.FileName() // EPIC-071: preserve original filename
				ext := filepath.Ext(part.FileName())
				if ext == "" {
					ext = ".m4a"
				}
				tmp, err := os.CreateTemp("", "linkari-file-*"+ext)
				if err != nil {
					slog.ErrorContext(ctx, "share rejected: create temp file failed", "error", err)
					writeError(w, http.StatusInternalServerError, "internal error")
					return
				}
				audioWritten, err = io.Copy(tmp, part)
				tmp.Close()
				if err != nil {
					os.Remove(tmp.Name())
					slog.ErrorContext(ctx, "share rejected: write temp file failed", "error", err)
					// MaxBytesReader returns a specific error on overflow.
					writeError(w, http.StatusRequestEntityTooLarge, "file too large")
					return
				}
				req.AudioPath = tmp.Name()
			default:
				// Drain unknown parts.
				io.Copy(io.Discard, part)
			}
			part.Close()
		}

		if req.AudioPath == "" {
			slog.DebugContext(ctx, "share rejected: no file part")
			writeError(w, http.StatusBadRequest, "file part required")
			return
		}

		// EPIC-038 M3: derive req.Type from the MIME type sent by Android.
		if req.MimeType != "" {
			switch {
			case strings.HasPrefix(req.MimeType, "audio/"):
				req.Type = "audio"
			case strings.HasPrefix(req.MimeType, "image/"):
				req.Type = "image"
			case req.MimeType == "application/pdf":
				req.Type = "document"
			}
		}

		// EPIC-038 M3: populate Filename from multipart header if not sent
		// as a dedicated form field.
		if req.Filename == "" && req.OriginalFilename != "" {
			req.Filename = req.OriginalFilename
		}

		// EPIC-078 M2: run screenshot detection synchronously after all
		// multipart fields are populated. detectScreenshot was previously
		// only called from the async scoring goroutine — non-MediaStore URIs
		// (e.g. Samsung Gallery) never set is_screenshot=true on the queue row.
		detectScreenshot(&req)

		slog.DebugContext(ctx, "share parsed (multipart)",
			"type", req.Type, "action", req.Action, "audio_size", audioWritten,
			"temp_path", req.AudioPath, "mime_type", req.MimeType,
			"calling_package", req.CallingPackage, "profile", req.Profile,
			"filename", req.Filename, "relative_path", req.RelativePath,
			"is_screenshot", req.IsScreenshot, "file_size", req.FileSize,
		)
		if s.debug {
			if raw, err := json.MarshalIndent(req, "", "  "); err == nil {
				fmt.Fprintf(s.ring.Writer(), "DEBUG share payload (multipart):\n%s\n", raw)
			}
		}
	} else {
		// Existing JSON path.
		r.Body = http.MaxBytesReader(w, r.Body, maxPayloadSize)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			slog.DebugContext(ctx, "share rejected: JSON decode error", "error", err)
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
			return
		}
		slog.DebugContext(ctx, "share parsed",
			"type", req.Type, "action", req.Action, "profile", req.Profile,
			"target", req.Target, "enter", req.Enter,
			"text_len", len(req.Text), "url_len", len(req.URL), "title", req.Title,
		)
		if s.debug {
			if raw, err := json.MarshalIndent(req, "", "  "); err == nil {
				fmt.Fprintf(s.ring.Writer(), "DEBUG share payload (debug):\n%s\n", raw)
			}
		}
	}

	// EPIC-067: if we have a temp audio file and exit before scoreAudioAsync
	// takes ownership, clean it up. The flag is cleared after successful routing.
	audioCleanup := req.AudioPath
	defer func() {
		if audioCleanup != "" {
			os.Remove(audioCleanup)
		}
	}()

	shareStart := time.Now()

	// Validate
	if err := validateRequest(&req); err != nil {
		slog.DebugContext(ctx, "share rejected: validation error", "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// EPIC-077 M2: Jira reroute fires first — before scoped-auth and before
	// resolveShareAction. Ordering invariant:
	//   1. routeJiraURL (uinit_auto + Jira URL → ginit_auto)
	//   2. checkScopedAuth (sees post-reroute action — mobile token rejected for ginit_*)
	//   3. resolveShareAction (full caller-wins resolution, minus Jira rerouting)
	//
	// This makes the Jira Ingress Invariant (EPIC-057) explicit and independently
	// testable. routeJiraURL mutates req.Action when rerouting applies.
	if s.router.RouteJiraURL(&req) {
		slog.DebugContext(ctx, "share: jira auto-rerouted to ginit_auto",
			"event_type", "share_jira_rerouted",
			"url", req.URL,
		)
	}

	// EPIC-052: resolve (action, profile) provenance BEFORE any DB write so
	// the share_action_resolved event lands even if the Enqueue below fails.
	// The caller-wins invariant is enforced inside resolveShareAction — when
	// s.shareHeuristicOverride is false (the default), received_action wins
	// unconditionally. The resolved values are written back onto req so the
	// queue row and downstream Route see the same resolution the event
	// records.
	resolution := s.router.ResolveShare(&req, s.shareHeuristicOverride)
	req.Action = resolution.ResolvedAction
	req.Profile = resolution.ResolvedProfile

	// EPIC-057: scoped-auth — verify the bearer token is authorized for
	// the resolved action. Mobile tokens cannot invoke ginit_*; Jira tokens
	// can only invoke ginit_*.
	tokenKind, scopeOK := s.checkScopedAuth(bearer, req.Action)
	if !scopeOK {
		resolution.Reason = "rejected_scope_violation"
		emitShareActionResolved(resolution, req.URL, 0)
		slog.WarnContext(ctx, "share rejected: scope violation",
			"token_kind", tokenKind, "action", req.Action)
		writeError(w, http.StatusForbidden, "action not permitted for this token")
		return
	}

	// EPIC-057: Jira key regex validation for ginit_* actions.
	if strings.HasPrefix(req.Action, "ginit_") && !jiraKeyRegex.MatchString(req.Text) {
		resolution.Reason = "rejected_invalid_jira_key"
		emitShareActionResolved(resolution, req.URL, 0)
		slog.WarnContext(ctx, "share rejected: invalid Jira key",
			"action", req.Action, "text", req.Text)
		writeError(w, http.StatusBadRequest, "invalid Jira issue key")
		return
	}

	emitShareActionResolved(resolution, req.URL, 0)

	// EPIC-085 M2: thread per-action ForceContentClassify into the request so
	// scoreAsync can gate content-LLM reclassification on it.
	if ac := s.router.LookupAction(req.Action); ac != nil {
		req.ForceContentClassify = ac.ForceContentClassify
	}

	// EPIC-077 M1: synchronous fast-cascade classification pre-enqueue.
	// Runs the pure, IO-free stages (<1ms total) so classify_source is persisted
	// on the queue row at enqueue time. The slow stage (classifyContentProfile
	// via Haiku LLM) remains async inside scoreURLAsync/scoreFileAsync goroutines.
	//
	// Two-phase classification model:
	//   Phase 1 (sync, pre-enqueue, <1ms): intent_metadata → filename →
	//     subject_keywords → relative_path → url_domain heuristics.
	//   Phase 2 (async, post-enqueue): Haiku content classification override.
	//
	// Profile is only set here if it was not already resolved by
	// resolveShareAction (e.g. caller-wins for prefix-mapped actions).
	// EPIC-079 M2: unified sync cascade (stages 1-5 only, no LLM).
	if req.Profile == "" {
		req.Profile, req.ClassifySource = classifyShareRequestFast(&req)
	} else {
		req.ClassifySource = "caller"
	}
	// Emit classify_stage_win for pre-enqueue telemetry (EPIC-079 M2).
	if s.events != nil && req.ClassifySource != "" {
		_ = s.events.Emit("classify_stage_win", map[string]interface{}{
			"url":             req.URL,
			"profile":         req.Profile,
			"classify_source": req.ClassifySource,
			"stage":           classifySourceToStage(req.ClassifySource),
			"phase":           "pre_enqueue",
			"content_type":    req.Type,
		})
	}

	// EPIC-078 M5: pre-enqueue dedup for file shares. When the same filename
	// and file size have been enqueued within the last 5 minutes, return the
	// existing row ID immediately rather than creating a duplicate queue entry.
	// Applies only to file shares (non-empty Filename + positive FileSize).
	// URL shares use a separate dedup path via ScoreByURL.
	const fileDedupWindow = 5 * time.Minute
	if s.queue != nil && req.Filename != "" && req.FileSize > 0 {
		if existing, err := s.queue.FindRecentFile(req.Filename, req.FileSize, fileDedupWindow); err != nil {
			slog.WarnContext(ctx, "share: FindRecentFile error (dedup skipped)", "error", err)
		} else if existing != nil {
			slog.InfoContext(ctx, "share: duplicate file share suppressed",
				"event_type", "share_file_dedup",
				"existing_id", existing.ID,
				"filename", req.Filename,
				"file_size", req.FileSize,
			)
			writeJSON(w, http.StatusOK, ShareResponse{
				Status:    "ok",
				Message:   "duplicate",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				ID:        existing.ID,
				Duplicate: true,
			})
			return
		}
	}

	// EPIC-085 M1: synchronous login-wall pre-filter. Moved from scoreAsync to
	// handleShare so login-walled URLs are rejected before enqueue — no orphaned
	// queue rows, honest HTTP response to the client.
	if req.Type == "url" && isLoginWallDomain(req.URL) {
		slog.InfoContext(ctx, "share: login-wall domain pre-filtered",
			"event_type", "share_prefilter_login_wall",
			"url", req.URL,
		)
		if s.events != nil {
			_ = s.events.Emit("score_prefilter_skip", map[string]interface{}{
				"url":   req.URL,
				"stage": "login_wall_domain",
				"phase": "pre_enqueue",
			})
		}
		if s.queue != nil {
			enqueuePrefilterPush(s.queue, &req, "login_wall_domain")
		}
		writeJSON(w, http.StatusOK, ShareResponse{
			Status:    "ok",
			Message:   "Login-walled site — not scored",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	// EPIC-087 M1: synchronous unsupported-pipeline pre-filter. Mirrors the
	// login-wall pattern added in EPIC-085 M1 — reject before enqueue so no
	// orphaned queue rows are created and the client gets an honest response.
	// EPIC-009 M2: YouTube URLs bypass this gate — they match youTubeRE and
	// are routed to scoreYouTubeAsync by handleTemplate instead of being rejected.
	if req.Type == "url" && !isYouTubeURL(req.URL) && unsupportedPipelineRE.MatchString(req.URL) {
		slog.InfoContext(ctx, "share: unsupported pipeline pre-filtered",
			"event_type", "share_prefilter_unsupported_pipeline",
			"url", req.URL,
		)
		if s.events != nil {
			_ = s.events.Emit("score_prefilter_skip", map[string]interface{}{
				"url":   req.URL,
				"stage": "unsupported_pipeline",
				"phase": "pre_enqueue",
			})
		}
		if s.queue != nil {
			enqueuePrefilterPush(s.queue, &req, "unsupported_pipeline")
		}
		writeJSON(w, http.StatusOK, ShareResponse{
			Status:    "ok",
			Message:   "Video platform — not yet supported",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	// Enqueue for persistence (before routing — survives tmux failures).
	// EPIC-057: actions with AutoScore=true are enqueued as pre-scored so the
	// RelayedWatchdog never sweeps them.
	var queueID int64
	if s.queue != nil {
		ac := s.router.LookupAction(req.Action)
		if ac != nil && ac.AutoScore {
			id, err := s.queue.EnqueueScored(&req, "workspace_bootstrapped")
			if err != nil {
				slog.WarnContext(ctx, "queue enqueue (auto-scored) failed", "error", err)
			} else {
				queueID = id
				slog.DebugContext(ctx, "queue enqueued (auto-scored)", "id", id)
			}
		} else {
			id, err := s.queue.Enqueue(&req)
			if err != nil {
				slog.WarnContext(ctx, "queue enqueue failed", "error", err)
			} else {
				queueID = id
				slog.DebugContext(ctx, "queue enqueued", "id", id)
			}
		}
	}

	// EPIC-067: thread queue row ID for audio scoring (no URL to match on).
	req.QueueRowID = queueID

	// Route
	result, err := s.router.Route(&req)
	if err != nil {
		s.emitShareEvent(&req, "failure", shareStart, "")
		// If queue is active, return 200 "queued" instead of 500 —
		// the replay goroutine will retry when tmux is available.
		if s.queue != nil {
			slog.InfoContext(ctx, "share queued: routing failed",
				"event_type", "share_queued",
				"type", req.Type,
				"profile", req.Profile,
				"error", err.Error(),
			)
			writeJSON(w, http.StatusOK, ShareResponse{
				Status:         "queued",
				Message:        "tmux unavailable, queued for replay",
				Timestamp:      time.Now().UTC().Format(time.RFC3339),
				ID:             queueID,
				Slug:           urlToSlug(req.URL),
				ClassifySource: req.ClassifySource,
			})
			return
		}
		slog.ErrorContext(ctx, "share routing failed",
			"event_type", "share_error",
			"type", req.Type,
			"error", err.Error(),
		)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// EPIC-067: scoreAudioAsync now owns the temp file — disarm cleanup.
	audioCleanup = ""

	// Mark as relayed immediately since routing succeeded.
	// Uses the exact enqueued ID to prevent replay goroutine from re-processing.
	if s.queue != nil && queueID > 0 {
		s.queue.MarkRelayed(queueID)
	}

	s.emitShareEvent(&req, "success", shareStart, req.URL)

	slog.InfoContext(ctx, "share handled",
		"event_type", "share_handled",
		"type", req.Type,
		"profile", req.Profile,
		"title", req.Title,
		"result", result,
		"queue_id", queueID,
		"filename", req.Filename,
	)
	writeJSON(w, http.StatusOK, ShareResponse{
		Status:         "ok",
		Message:        result,
		Timestamp:      time.Now().UTC().Format(time.RFC3339),
		ID:             queueID,
		Slug:           urlToSlug(req.URL),
		ClassifySource: req.ClassifySource,
	})
}

// parseListParams extracts and validates the shared list query params used by
// /archive and /queue: profile, status, before_id, limit, type. Returns a
// 400-ready error describing the first invalid field.
func parseListParams(r *http.Request) (profile, status, itemType string, beforeID int64, limit int, filter *ArchiveFilter, err error) {
	profile = r.URL.Query().Get("profile")
	status = r.URL.Query().Get("status")
	if status != "" && !validStatuses[status] {
		return "", "", "", 0, 0, nil, fmt.Errorf("invalid status")
	}
	// EPIC-057: ?type=jira filters to ginit_* actions; ?type=url filters to
	// non-ginit actions. Empty means no filter.
	itemType = r.URL.Query().Get("type")
	if itemType != "" && itemType != "jira" && itemType != "url" {
		return "", "", "", 0, 0, nil, fmt.Errorf("invalid type: must be 'jira' or 'url'")
	}
	limit = 50
	if l := r.URL.Query().Get("limit"); l != "" {
		n, e := strconv.Atoi(l)
		if e != nil || n <= 0 || n > 200 {
			return "", "", "", 0, 0, nil, fmt.Errorf("invalid limit")
		}
		limit = n
	}
	if b := r.URL.Query().Get("before_id"); b != "" {
		n, e := strconv.ParseInt(b, 10, 64)
		if e != nil || n < 0 {
			return "", "", "", 0, 0, nil, fmt.Errorf("invalid before_id")
		}
		beforeID = n
	}
	// EPIC-070 M4: score and date range filters.
	var f ArchiveFilter
	hasFilter := false
	if s := r.URL.Query().Get("score_min"); s != "" {
		n, e := strconv.Atoi(s)
		if e != nil {
			return "", "", "", 0, 0, nil, fmt.Errorf("invalid score_min")
		}
		f.ScoreMin = &n
		hasFilter = true
	}
	if s := r.URL.Query().Get("score_max"); s != "" {
		n, e := strconv.Atoi(s)
		if e != nil {
			return "", "", "", 0, 0, nil, fmt.Errorf("invalid score_max")
		}
		f.ScoreMax = &n
		hasFilter = true
	}
	if s := r.URL.Query().Get("since"); s != "" {
		if _, e := time.Parse(time.RFC3339, s); e != nil {
			return "", "", "", 0, 0, nil, fmt.Errorf("invalid since: must be RFC3339")
		}
		f.Since = s
		hasFilter = true
	}
	if s := r.URL.Query().Get("until"); s != "" {
		if _, e := time.Parse(time.RFC3339, s); e != nil {
			return "", "", "", 0, 0, nil, fmt.Errorf("invalid until: must be RFC3339")
		}
		f.Until = s
		hasFilter = true
	}
	// EPIC-072 M7: cluster_id filter.
	if s := r.URL.Query().Get("cluster_id"); s != "" {
		n, e := strconv.ParseInt(s, 10, 64)
		if e != nil {
			return "", "", "", 0, 0, nil, fmt.Errorf("invalid cluster_id")
		}
		f.ClusterID = &n
		hasFilter = true
	}
	if hasFilter {
		filter = &f
	}
	return
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !s.authenticateRequest(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if s.queue == nil {
		writeError(w, http.StatusServiceUnavailable, "queue not configured")
		return
	}

	_, status, _, beforeID, limit, _, perr := parseListParams(r)
	if perr != nil {
		writeError(w, http.StatusBadRequest, perr.Error())
		return
	}

	items, err := s.queue.ListCursor(status, beforeID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("queue list: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

type scoreRequest struct {
	Score        int            `json:"score"`
	Slug         string         `json:"slug"`
	Tags         string         `json:"tags"`
	Verdict      string         `json:"verdict"`
	RubricScores map[string]int `json:"rubric_scores,omitempty"`
}

func (s *Server) handleQueueScore(w http.ResponseWriter, r *http.Request) {
	if !s.authenticateRequest(r) {
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

	if err := s.queue.UpdateScore(id, req.Score, req.Tags, req.Verdict, req.Slug, "", "", req.RubricScores); err != nil {
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
		slog.InfoContext(r.Context(), "archive",
			"event_type", "archive",
			"id", id,
			"score", req.Score,
			"profile", item.Profile,
			"tags", req.Tags,
		)
	} else {
		slog.InfoContext(r.Context(), "scored",
			"event_type", "scored",
			"id", id,
			"score", req.Score,
			"profile", item.Profile,
			"threshold", threshold,
		)
	}

	// EPIC-059: push notifications are decoupled from archive gate.
	// Every scored item produces a push regardless of whether it meets the
	// archive threshold. Throttle, min-score floor, and cross-process race
	// guard all live in Queue.EnqueueDigestIfDue (EPIC-051 invariant).
	s.enqueueDigestPush(r.Context(), item.Profile, req.Score, req.Slug, req.Verdict, "")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(item)
}

// handleQueueOutcome handles POST /queue/{id}/outcome (EPIC-070 M1).
func (s *Server) handleQueueOutcome(w http.ResponseWriter, r *http.Request) {
	if !s.authenticateRequest(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.queue == nil {
		writeError(w, http.StatusServiceUnavailable, "queue not configured")
		return
	}

	idStr := r.PathValue("id")
	var id int64
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid queue item ID")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxPayloadSize)
	var req struct {
		Outcome string `json:"outcome"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if !validOutcomes[req.Outcome] {
		writeError(w, http.StatusBadRequest, "invalid outcome value")
		return
	}

	if err := s.queue.UpdateOutcome(id, req.Outcome); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("update outcome: %v", err))
		return
	}

	item, err := s.queue.GetByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("get item: %v", err))
		return
	}

	slog.InfoContext(r.Context(), "outcome",
		"event_type", "outcome_recorded",
		"id", id,
		"outcome", req.Outcome,
		"profile", item.Profile,
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(item)
}

// handleQueueFeedback handles POST /queue/{id}/feedback (EPIC-070 M2).
func (s *Server) handleQueueFeedback(w http.ResponseWriter, r *http.Request) {
	if !s.authenticateRequest(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.queue == nil {
		writeError(w, http.StatusServiceUnavailable, "queue not configured")
		return
	}

	idStr := r.PathValue("id")
	var id int64
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid queue item ID")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxPayloadSize)
	var req struct {
		Feedback string `json:"feedback"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if err := s.queue.UpdateFeedback(id, req.Feedback); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("update feedback: %v", err))
		return
	}

	item, err := s.queue.GetByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("get item: %v", err))
		return
	}

	slog.InfoContext(r.Context(), "feedback",
		"event_type", "feedback_recorded",
		"id", id,
		"feedback", req.Feedback,
		"profile", item.Profile,
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(item)
}

// resolveSlugToID looks up a queue item by slug and returns its ID (EPIC-072 M1).
func (s *Server) resolveSlugToID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if !s.authenticateRequest(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return 0, false
	}
	if s.queue == nil {
		writeError(w, http.StatusServiceUnavailable, "queue not configured")
		return 0, false
	}
	slug := r.PathValue("slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "empty slug")
		return 0, false
	}
	item, err := s.queue.GetBySlug(slug)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("slug lookup: %v", err))
		return 0, false
	}
	return item.ID, true
}

// handleQueueFeedbackBySlug handles POST /queue/slug/{slug}/feedback (EPIC-072 M1).
func (s *Server) handleQueueFeedbackBySlug(w http.ResponseWriter, r *http.Request) {
	id, ok := s.resolveSlugToID(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPayloadSize)
	var req struct {
		Feedback string `json:"feedback"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if err := s.queue.UpdateFeedback(id, req.Feedback); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("update feedback: %v", err))
		return
	}

	item, err := s.queue.GetByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("get item: %v", err))
		return
	}

	slog.InfoContext(r.Context(), "feedback",
		"event_type", "feedback_recorded",
		"id", id,
		"feedback", req.Feedback,
		"profile", item.Profile,
		"via", "slug",
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(item)
}

// handleQueueOutcomeBySlug handles POST /queue/slug/{slug}/outcome (EPIC-072 M1).
func (s *Server) handleQueueOutcomeBySlug(w http.ResponseWriter, r *http.Request) {
	id, ok := s.resolveSlugToID(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPayloadSize)
	var req struct {
		Outcome string `json:"outcome"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if !validOutcomes[req.Outcome] {
		writeError(w, http.StatusBadRequest, "invalid outcome value")
		return
	}

	if err := s.queue.UpdateOutcome(id, req.Outcome); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("update outcome: %v", err))
		return
	}

	item, err := s.queue.GetByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("get item: %v", err))
		return
	}

	slog.InfoContext(r.Context(), "outcome",
		"event_type", "outcome_recorded",
		"id", id,
		"outcome", req.Outcome,
		"profile", item.Profile,
		"via", "slug",
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(item)
}

// handleProfileStats handles GET /profiles/stats (EPIC-070 M2).
func (s *Server) handleProfileStats(w http.ResponseWriter, r *http.Request) {
	if !s.authenticateRequest(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.queue == nil {
		writeError(w, http.StatusServiceUnavailable, "queue not configured")
		return
	}

	profile := r.URL.Query().Get("profile")
	stats, err := s.queue.ProfileStats(profile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("profile stats: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// enqueueDigestPush is a thin server-side wrapper around
// Queue.EnqueueDigestIfDue that emits the matching observability events.
// EPIC-051 M3 replaces the deleted maybeDigestPush helper. The helper itself
// (not this wrapper) is the single sanctioned entry point for digest rows.
func (s *Server) enqueueDigestPush(ctx context.Context, profile string, score int, slug, verdict, url string) {
	if s.queue == nil {
		return
	}
	res, err := s.queue.EnqueueDigestIfDue(ctx, profile, score, slug, verdict, url)
	if err != nil {
		slog.WarnContext(ctx, "digest enqueue failed", "error", err)
		return
	}
	switch {
	case res.Enqueued:
		emitPushEvent("digest_push_enqueued", map[string]interface{}{
			"id": res.ID, "profile": profile, "score": score, "slug": slug,
			"throttle_remaining_ms": res.ThrottleRemainingMs,
		})
		slog.DebugContext(ctx, "digest push enqueued",
			"event_type", "digest_push_enqueued",
			"id", res.ID, "profile", profile, "score", score, "slug", slug,
		)
	case res.Reason == "throttled":
		emitPushEvent("digest_push_throttled", map[string]interface{}{
			"profile": profile, "score": score, "slug": slug,
			"seconds_until_next_allowed": res.SecondsUntilAllowed,
		})
		slog.DebugContext(ctx, "digest push throttled",
			"event_type", "digest_push_throttled",
			"profile", profile, "score", score, "slug", slug,
			"seconds_until_next_allowed", res.SecondsUntilAllowed,
		)
	case res.Reason == "below_min_score":
		slog.DebugContext(ctx, "digest push suppressed: below min score",
			"event_type", "digest_push_below_min_score",
			"profile", profile, "score", score, "slug", slug,
		)
	}
}

func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authenticateRequest(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.queue == nil {
		writeError(w, http.StatusServiceUnavailable, "queue not configured")
		return
	}

	profile, status, itemType, beforeID, limit, filter, perr := parseListParams(r)
	if perr != nil {
		writeError(w, http.StatusBadRequest, perr.Error())
		return
	}

	items, err := s.queue.ListArchivedCursorTyped(profile, status, itemType, beforeID, limit, filter)
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
	if !s.authenticateRequest(r) {
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

	// EPIC-072 M7: cluster-aware digest response gated by ?clusters=1.
	if r.URL.Query().Get("clusters") == "1" {
		clusters, cerr := s.queue.ListClusters(profile)
		if cerr != nil {
			slog.Warn("digest clusters failed", "error", cerr)
		}
		// Enrich clusters with tag intersection.
		var enriched []ClusterGroup
		for _, c := range clusters {
			cItems, _ := s.queue.GetClusterItems(c.ID)
			if len(cItems) > 0 {
				c.Tags = parseTags(cItems[0].TopicTags)
				for _, ci := range cItems[1:] {
					c.Tags = tagIntersection(c.Tags, parseTags(ci.TopicTags))
				}
				c.ItemIDs = make([]int64, len(cItems))
				for i, ci := range cItems {
					c.ItemIDs[i] = ci.ID
				}
			}
			enriched = append(enriched, c)
		}
		type DigestResponse struct {
			Items    []QueueItem    `json:"items"`
			Clusters []ClusterGroup `json:"clusters,omitempty"`
		}
		json.NewEncoder(w).Encode(DigestResponse{Items: items, Clusters: enriched})
		return
	}

	json.NewEncoder(w).Encode(items)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if !s.authenticateRequest(r) {
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
	ctx := r.Context()
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Auth — same bearer token as other endpoints.
	if !s.authenticateRequest(r) {
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
			slog.WarnContext(ctx, "device upsert failed", "error", err)
		} else {
			emitPushEvent("push_register_upsert", map[string]interface{}{
				"token_len": len(req.FCMToken),
			})
		}
	}

	slog.InfoContext(ctx, "FCM token registered",
		"event_type", "fcm_register",
		"token_len", len(req.FCMToken),
	)

	writeJSON(w, http.StatusOK, ShareResponse{
		Status:    "ok",
		Message:   "token registered",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}

// testPushResponse is the JSON body returned by POST /push/test. EPIC-056 M3.
type testPushResponse struct {
	Status    string `json:"status"`          // "ok" | "error"
	Timestamp string `json:"timestamp"`       // RFC3339 UTC
	Error     string `json:"error,omitempty"` // populated on failure
	Reason    string `json:"reason,omitempty"`
}

// handleTestPush synchronously fires a single FCM notification to the
// currently-registered device, bypassing push_outbox, throttle, and
// min-score gating. EPIC-056 M3. Diagnostic-only — must NEVER touch
// push_outbox or share the throttle state.
func (s *Server) handleTestPush(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if !s.authenticateRequest(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if s.queue == nil {
		emitPushEvent("push_test_failed", map[string]interface{}{"reason": "queue_unavailable"})
		writeError(w, http.StatusServiceUnavailable, "queue unavailable")
		return
	}

	deviceToken, err := s.queue.GetDeviceToken()
	if err != nil {
		slog.WarnContext(ctx, "test push: device token lookup failed", "error", err)
		emitPushEvent("push_test_failed", map[string]interface{}{
			"reason": "device_lookup_error",
			"error":  err.Error(),
		})
		writeError(w, http.StatusInternalServerError, "device lookup failed")
		return
	}
	if deviceToken == "" {
		emitPushEvent("push_test_failed", map[string]interface{}{"reason": "no_device_registered"})
		writeJSON(w, http.StatusBadRequest, testPushResponse{
			Status:    "error",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Error:     "no device registered; call /register first",
			Reason:    "no_device_registered",
		})
		return
	}

	const (
		testScore   = 75
		testSlug    = "test"
		testVerdict = "Test notification from Linkari settings."
		testURL     = "https://linkari.test/ping"
	)

	if err := sendOutboxFCM(s, deviceToken, testScore, testSlug, testVerdict, testURL, "", "", "", ""); err != nil {
		slog.WarnContext(ctx, "test push: FCM send failed", "error", err)
		emitPushEvent("push_test_failed", map[string]interface{}{
			"reason":    "fcm_send_failed",
			"error":     err.Error(),
			"token_len": len(deviceToken),
		})
		writeJSON(w, http.StatusBadGateway, testPushResponse{
			Status:    "error",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Error:     err.Error(),
			Reason:    "fcm_send_failed",
		})
		return
	}

	emitPushEvent("push_test_sent", map[string]interface{}{
		"score":     testScore,
		"slug":      testSlug,
		"token_len": len(deviceToken),
	})
	writeJSON(w, http.StatusOK, testPushResponse{
		Status:    "ok",
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
	ctx := r.Context()
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Auth
	if !s.authenticateRequest(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxPayloadSize)
	var req notifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	// Always log the notification as a structured event.
	slog.InfoContext(ctx, "notify",
		"event_type", "notify",
		"score", req.Score,
		"profile", req.Profile,
		"url", req.URL,
		"slug", req.Slug,
		"verdict_len", len(req.Verdict),
	)

	// Persist score + verdict to queue and auto-archive if threshold met.
	if s.queue != nil && req.URL != "" {
		item, _, err := s.queue.ScoreByURL(req.URL, req.Score, req.Verdict, req.Tags, req.Profile, req.Slug, "", "")
		if err != nil {
			slog.WarnContext(ctx, "notify queue persist failed", "error", err)
		} else {
			at := archiveThreshold(req.Profile)
			if at >= 0 && item.Score != nil && *item.Score >= at {
				if archErr := s.queue.Archive(item.ID); archErr == nil {
					slog.DebugContext(ctx, "auto-archived item",
						"id", item.ID, "score", *item.Score, "threshold", at,
					)
				}
			}
		}
	}

	// EPIC-059: removed redundant archive-threshold early return that predated
	// EPIC-051's unification. EnqueueDigestIfDue already enforces notify_min_score
	// and per-profile throttle — the old gate here silently dropped pushes for
	// profiles like "life" (threshold=-1).

	if s.queue == nil {
		writeError(w, http.StatusServiceUnavailable, "queue not configured")
		return
	}

	// EPIC-051 M3: /notify is one of three writer paths now unified behind
	// Queue.EnqueueDigestIfDue. The helper applies the configured min-score
	// floor and per-profile throttle; this endpoint remains wired for any
	// legacy caller that might still POST to it.
	s.enqueueDigestPush(ctx, req.Profile, req.Score, req.Slug, req.Verdict, req.URL)
	writeJSON(w, http.StatusOK, ShareResponse{
		Status:    "ok",
		Message:   fmt.Sprintf("push enqueued (score=%d)", req.Score),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}

// fcmEndpoint is the FCM HTTP v1 API endpoint (used by the push outbox worker).
const fcmEndpoint = "https://fcm.googleapis.com/v1/projects/bloinlagr/messages:send"

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
	case "audio", "image", "document":
		if req.AudioPath == "" {
			return fmt.Errorf("file required for type=%s", req.Type)
		}
		fi, err := os.Stat(req.AudioPath)
		if err != nil {
			return fmt.Errorf("file not accessible: %w", err)
		}
		if fi.Size() > maxAudioSize {
			return fmt.Errorf("file too large: %d bytes (max %d)", fi.Size(), maxAudioSize)
		}
	default:
		return fmt.Errorf("unsupported type %q (expected text, url, audio, image, or document)", req.Type)
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
		"profile":         req.Profile,
		"url_domain":      domainFromURL(rawURL),
		"status":          status,
		"duration_ms":     time.Since(start).Milliseconds(),
		"type":            req.Type,
		"row_id":          req.QueueRowID,
		"classify_source": req.ClassifySource,
	}
	if err := s.events.Emit("linkari_share", meta); err != nil {
		slog.Warn("event emit linkari_share failed", "error", err)
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
		slog.Warn("event emit linkari_digest failed", "error", err)
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
