package main

import (
	"fmt"
	"log"
	"strings"
)

// Router dispatches share requests to the appropriate handler based on payload type.
type Router struct {
	tmux     *TmuxRunner
	handlers map[string]Handler
	debug    bool
}

// Handler processes a share request and returns a result message.
type Handler interface {
	Handle(req *ShareRequest, tmux *TmuxRunner) (string, error)
}

// NewRouter creates a router with default handlers for text and url types.
func NewRouter(tmux *TmuxRunner, debug bool) *Router {
	r := &Router{
		tmux:     tmux,
		handlers: make(map[string]Handler),
		debug:    debug,
	}
	r.handlers["text"] = &TextHandler{}
	r.handlers["url"] = &URLHandler{}
	return r
}

// Route dispatches a request to the appropriate handler.
func (r *Router) Route(req *ShareRequest) (string, error) {
	h, ok := r.handlers[req.Type]
	if !ok {
		return "", fmt.Errorf("no handler for type %q", req.Type)
	}
	if r.debug {
		log.Printf("[DEBUG] route: type=%q → %T", req.Type, h)
	}
	return h.Handle(req, r.tmux)
}

// TextHandler pastes text literally into tmux. No execution unless Enter is requested.
type TextHandler struct{}

func (h *TextHandler) Handle(req *ShareRequest, tmux *TmuxRunner) (string, error) {
	target := resolveTarget(req.Target, tmux.DefaultSession)

	if err := tmux.SendKeys(target, req.Text, req.Enter); err != nil {
		return "", err
	}
	return fmt.Sprintf("Sent to %s", target), nil
}

// URLHandler routes URLs to uinit via tmux. Always sends Enter to execute.
type URLHandler struct{}

func (h *URLHandler) Handle(req *ShareRequest, tmux *TmuxRunner) (string, error) {
	// Parse session name from target ("session:pane" → "session"), falling back to default.
	session := tmux.DefaultSession
	if req.Target != "" {
		session = strings.Split(req.Target, ":")[0]
	}

	// Shell-safe: only pass validated URLs (http/https prefix enforced in validation).
	// Quote the URL to prevent shell interpretation of special characters.
	command := fmt.Sprintf("uinit %s", shellQuote(req.URL))

	// Open a dedicated tmux window for each URL share.
	if err := tmux.NewWindow(session, command); err != nil {
		return "", err
	}
	return fmt.Sprintf("Opened new window in %s", session), nil
}

// resolveTarget returns the explicit target or falls back to default session:0.
func resolveTarget(target, defaultSession string) string {
	if target != "" {
		return target
	}
	return defaultSession
}

// shellQuote wraps a string in single quotes, escaping embedded single quotes.
// This prevents shell injection via URL payloads.
func shellQuote(s string) string {
	// Replace ' with '\'' (end quote, escaped quote, start quote)
	escaped := strings.ReplaceAll(s, "'", "'\\''")
	return "'" + escaped + "'"
}
