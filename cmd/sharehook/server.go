package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// maxPayloadSize limits request body to 64KB.
const maxPayloadSize = 64 * 1024

// ShareRequest is the incoming payload from Android HTTP Shortcuts.
type ShareRequest struct {
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
	URL    string `json:"url,omitempty"`
	Target string `json:"target,omitempty"`
	Enter  bool   `json:"enter"`
}

// ShareResponse is the structured JSON response.
type ShareResponse struct {
	Status    string `json:"status"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

// Server handles HTTP requests with authentication and rate limiting.
type Server struct {
	token   string
	router  *Router
	limiter *rateLimiter
	debug   bool
}

// NewServer creates a new Server with the given bearer token and router.
func NewServer(token string, router *Router, debug bool) *Server {
	return &Server{
		token:   token,
		router:  router,
		limiter: newRateLimiter(30, time.Minute),
		debug:   debug,
	}
}

// Mux returns the HTTP handler mux.
func (s *Server) Mux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/share", s.handleShare)
	mux.HandleFunc("/healthz", s.handleHealth)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if s.debug {
		log.Printf("[DEBUG] %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ShareResponse{
		Status:    "ok",
		Message:   "healthy",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
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
		log.Printf("[DEBUG] parsed: type=%q target=%q enter=%t text_len=%d url_len=%d", req.Type, req.Target, req.Enter, len(req.Text), len(req.URL))
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
