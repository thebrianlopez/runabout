package main

import (
	"fmt"
	"log"
	"regexp"
	"strings"
)

// Action describes a share target exposed via GET /actions.
type Action struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Icon   string `json:"icon"`
	Type   string `json:"type"`
	Target string `json:"target"`
}

// Router dispatches share requests to the appropriate handler based on payload type.
type Router struct {
	tmux     *TmuxRunner
	handlers map[string]Handler
	actions  []Action
	debug    bool
}

// Handler processes a share request and returns a result message.
type Handler interface {
	Handle(req *ShareRequest, tmux *TmuxRunner) (string, error)
}

// NewRouter creates a router with default handlers for text and url types.
// callbackToken and callbackPort configure the score callback from uinit
// to POST /notify for FCM push notifications.
func NewRouter(tmux *TmuxRunner, debug bool, callbackToken string, callbackPort int) *Router {
	r := &Router{
		tmux:     tmux,
		handlers: make(map[string]Handler),
		debug:    debug,
	}
	r.handlers["text"] = &TextHandler{}
	r.handlers["url"] = &URLHandler{callbackToken: callbackToken, callbackPort: callbackPort}
	r.handlers["ginit"] = &GinitHandler{}

	r.actions = []Action{
		{ID: "uinit_eng", Label: "Linkari (Eng)", Icon: "eng", Type: "url", Target: "android-share:0"},
		{ID: "uinit_life", Label: "Linkari (Life)", Icon: "life", Type: "url", Target: "android-share:0"},
		{ID: "uinit_finance", Label: "Linkari (Finance)", Icon: "finance", Type: "url", Target: "android-share:0"},
		{ID: "note", Label: "Capture Note", Icon: "note", Type: "text", Target: "android-share:0"},
		{ID: "ginit", Label: "ginit", Icon: "work", Type: "text", Target: "android-share:0"},
	}

	return r
}

// Actions returns the registered share actions.
func (r *Router) Actions() []Action {
	return r.actions
}

// Route dispatches a request to the appropriate handler.
// When req.Action is set, the handler is looked up by action ID;
// otherwise it falls back to type-based routing.
// For uinit_* actions, the profile suffix is extracted and set on
// the request, then routing falls through to the "url" handler.
func (r *Router) Route(req *ShareRequest) (string, error) {
	key := req.Type
	if req.Action != "" {
		key = req.Action
	}

	// Extract profile from uinit_<profile> action IDs.
	// Bare "uinit" (legacy/default) maps to "url" handler with no profile.
	if key == "uinit" {
		key = "url"
	} else if strings.HasPrefix(key, "uinit_") {
		profile := strings.TrimPrefix(key, "uinit_")
		if req.Profile == "" {
			req.Profile = profile
		}
		key = "url"
	}

	h, ok := r.handlers[key]
	if !ok {
		if req.Action != "" {
			return "", fmt.Errorf("no handler for action %q", req.Action)
		}
		return "", fmt.Errorf("no handler for type %q", req.Type)
	}
	if r.debug {
		log.Printf("[DEBUG] route: key=%q profile=%q → %T", key, req.Profile, h)
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
// callbackToken and callbackPort are used to construct a curl callback to
// POST /notify after uinit completes, reporting the score for FCM push.
type URLHandler struct {
	callbackToken string
	callbackPort  int
}

func (h *URLHandler) Handle(req *ShareRequest, tmux *TmuxRunner) (string, error) {
	// Parse session name from target ("session:pane" → "session"), falling back to default.
	session := tmux.DefaultSession
	if req.Target != "" {
		session = strings.Split(req.Target, ":")[0]
	}

	// Shell-safe: only pass validated URLs (http/https prefix enforced in validation).
	// Quote the URL to prevent shell interpretation of special characters.
	//
	// After uinit completes, if $UINIT_SCORE is set, curl back to POST /notify
	// so linkari can trigger FCM push notifications for high scores.
	// The callback runs inside fish -c via tmux new-window, so we use fish
	// test syntax and fish variable expansion ($UINIT_SCORE, $UINIT_URL, $UINIT_SLUG).
	command := fmt.Sprintf("uinit %s", shellQuote(req.URL))
	if req.Profile != "" && req.Profile != "eng" {
		command = fmt.Sprintf("uinit --profile %s %s", req.Profile, shellQuote(req.URL))
	}
	if h.callbackToken != "" && h.callbackPort > 0 {
		callback := fmt.Sprintf(
			"; if test -n \"$UINIT_SCORE\"; curl -s -X POST http://localhost:%d/notify -H 'Authorization: Bearer %s' -H 'Content-Type: application/json' -d '{\"score\":'$UINIT_SCORE',\"url\":\"'$UINIT_URL'\",\"slug\":\"'$UINIT_SLUG'\"}'; end",
			h.callbackPort, h.callbackToken,
		)
		command += callback
	}

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

// jiraKeyRe matches Jira issue keys like ISRE-1234.
var jiraKeyRe = regexp.MustCompile(`[A-Z][A-Z0-9]+-[0-9]+`)

// GinitHandler parses a Jira key from shared text and runs ginit in a new tmux window.
type GinitHandler struct{}

func (h *GinitHandler) Handle(req *ShareRequest, tmux *TmuxRunner) (string, error) {
	key := jiraKeyRe.FindString(strings.TrimSpace(req.Text))
	if key == "" {
		return "", fmt.Errorf("no Jira key found in text %q", req.Text)
	}

	session := tmux.DefaultSession
	if req.Target != "" {
		session = strings.Split(req.Target, ":")[0]
	}

	command := fmt.Sprintf("ginit %s", key)
	if err := tmux.NewWindow(session, command); err != nil {
		return "", err
	}
	return fmt.Sprintf("ginit %s in %s", key, session), nil
}
