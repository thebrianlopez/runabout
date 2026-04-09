package main

import (
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"strings"
	"sync"
)

// archiveThresholdCache lazily loads the actions config once per process so
// the package-level `archiveThreshold` helper used by cmd_score / cmd_triage
// and the server FCM path doesn't re-parse actions.yaml on every call.
var (
	archiveThresholdOnce sync.Once
	archiveThresholdCfg  *Config
)

func loadArchiveThresholdConfig() *Config {
	archiveThresholdOnce.Do(func() {
		cfg, err := LoadConfig("")
		if err != nil {
			cfg = builtinConfig()
		}
		archiveThresholdCfg = cfg
	})
	return archiveThresholdCfg
}

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
	tmux       *TmuxRunner
	actions    []Action
	actionsCfg []ActionConfig
	cfgIndex   map[string]*ActionConfig
	debug      bool
	mu         sync.RWMutex
}

// Handler processes a share request and returns a result message.
type Handler interface {
	Handle(req *ShareRequest, tmux *TmuxRunner) (string, error)
}

// NewRouterFromConfig creates a config-driven router. Actions are loaded from cfg.
func NewRouterFromConfig(tmux *TmuxRunner, cfg *Config, debug bool) *Router {
	r := &Router{
		tmux:  tmux,
		debug: debug,
	}
	r.loadConfig(cfg)

	// Pre-create tmux sessions.
	for _, a := range r.actions {
		session := strings.Split(a.Target, ":")[0]
		if session != "" {
			if err := tmux.createSession(session); err != nil {
				slog.Warn("failed to pre-create tmux session", "session", session, "error", err)
			} else {
				slog.Debug("pre-created tmux session", "session", session)
			}
		}
	}

	return r
}

// loadConfig replaces the current action set from a Config. Thread-safe for hot-reload.
func (r *Router) loadConfig(cfg *Config) {
	active := cfg.ActiveActions()
	actions := make([]Action, 0, len(active))
	index := make(map[string]*ActionConfig, len(active))

	for i := range active {
		a := &active[i]
		// Apply default archive threshold if not set.
		if a.ArchiveThreshold == 0 && cfg.DefaultArchiveThreshold != 0 {
			a.ArchiveThreshold = cfg.DefaultArchiveThreshold
		}
		actions = append(actions, a.ToAction())
		index[a.ID] = a
	}

	r.mu.Lock()
	r.actionsCfg = active
	r.actions = actions
	r.cfgIndex = index
	r.mu.Unlock()
}

// Reload replaces the router's config from a new Config. Used for SIGHUP hot-reload.
func (r *Router) Reload(cfg *Config) {
	r.loadConfig(cfg)
	slog.Debug("router reloaded",
		"event_type", "router_reload",
		"action_count", len(r.actions),
	)
}

// Actions returns the registered share actions.
func (r *Router) Actions() []Action {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.actions
}

// ArchiveThreshold returns the archive threshold for a given action/profile
// using this router's live actions config. Falls back to the package-level
// `archiveThreshold` helper (also config-driven) when no in-memory action
// matches — this covers profiles not listed in actions.yaml but present in
// the cached default config.
func (r *Router) ArchiveThreshold(profile string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, a := range r.actionsCfg {
		if a.ProfileMap == "prefix" {
			suffix := strings.TrimPrefix(a.ID, "uinit_")
			if suffix == profile {
				return a.ArchiveThreshold
			}
		}
	}
	return archiveThreshold(profile)
}

// archiveThreshold returns the archive threshold for a profile using the
// actions config loaded from ~/.config/linkari/actions.yaml (or the builtin
// fallback). EPIC-043 M4: replaces the legacy hardcoded switch with a
// config-driven lookup so thresholds are edited in one place — the YAML.
// Unknown profiles fall back to the config's default_archive_threshold.
func archiveThreshold(profile string) int {
	cfg := loadArchiveThresholdConfig()
	for i := range cfg.Actions {
		a := &cfg.Actions[i]
		if a.ProfileMap != "prefix" {
			continue
		}
		if strings.TrimPrefix(a.ID, "uinit_") == profile {
			return a.ArchiveThreshold
		}
	}
	if cfg.DefaultArchiveThreshold != 0 {
		return cfg.DefaultArchiveThreshold
	}
	return 80
}

// Route dispatches a request using config-driven actions.
func (r *Router) Route(req *ShareRequest) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	actionID := req.Action
	if actionID == "" {
		actionID = req.Type
	}

	// Resolve target from action definition when the client doesn't send one.
	if req.Target == "" && actionID != "" {
		if ac, ok := r.cfgIndex[actionID]; ok {
			req.Target = ac.Target
		}
	}

	// Extract profile from action ID with profile_map=prefix.
	if ac, ok := r.cfgIndex[actionID]; ok {
		if ac.ProfileMap == "prefix" {
			parts := strings.SplitN(actionID, "_", 2)
			if len(parts) == 2 && req.Profile == "" {
				req.Profile = parts[1]
			}
			// Normalize bare prefix to first action of that prefix type.
		}
	} else {
		// Try prefix matching for uinit_<profile> pattern.
		if strings.HasPrefix(actionID, "uinit_") {
			profile := strings.TrimPrefix(actionID, "uinit_")
			if req.Profile == "" {
				req.Profile = profile
			}
			// Look for a uinit_eng or similar template action to use.
			for id, ac := range r.cfgIndex {
				if ac.ProfileMap == "prefix" && strings.HasPrefix(id, "uinit_") {
					actionID = id
					req.Target = ac.Target
					break
				}
			}
		}
	}

	ac, ok := r.cfgIndex[actionID]
	if !ok {
		return "", fmt.Errorf("no action for %q", actionID)
	}

	slog.Debug("route decision",
		"event_type", "route_decision",
		"action", actionID,
		"kind", string(ac.Kind),
		"profile", req.Profile,
	)

	switch ac.Kind {
	case KindLiteral:
		return r.handleLiteral(ac, req)
	case KindTemplate:
		return r.handleTemplate(ac, req)
	case KindRegex:
		return r.handleRegex(ac, req)
	default:
		return "", fmt.Errorf("unknown action kind %q", ac.Kind)
	}
}

func (r *Router) handleLiteral(ac *ActionConfig, req *ShareRequest) (string, error) {
	if req.Target == "" {
		return "", fmt.Errorf("target is required for literal action %q", ac.ID)
	}
	if err := r.tmux.SendKeys(req.Target, req.Text, req.Enter); err != nil {
		return "", err
	}
	return "Locked in", nil
}

func (r *Router) handleTemplate(ac *ActionConfig, req *ShareRequest) (string, error) {
	data := TemplateData{
		URL:     req.URL,
		Text:    req.Text,
		Profile: req.Profile,
		Slug:    urlToSlug(req.URL),
	}

	command, err := ac.RenderCommand(data)
	if err != nil {
		return "", err
	}

	// EPIC-043 M5: inline_triage=true runs the rendered command headlessly
	// (no tmux window, no interactive review pane). Used for fire-and-forget
	// scoring where the score is consumed via /notify FCM push or the queue
	// DB rather than a human eyeballing a tmux window.
	if ac.InlineTriage {
		return r.handleInlineTriage(ac, command)
	}

	if req.Target == "" {
		return "", fmt.Errorf("target is required for template action %q", ac.ID)
	}
	session := strings.Split(req.Target, ":")[0]

	windowName := data.Slug
	if req.Profile != "" {
		windowName = fmt.Sprintf("%s: %s", req.Profile, data.Slug)
	}

	if err := r.tmux.NewWindow(session, command, windowName); err != nil {
		return "", err
	}
	return "Cooking... verdict drops soon", nil
}

// handleInlineTriage runs the rendered command via the configured shell with
// no tmux wrapper. The command is spawned detached (fire-and-forget) because
// triage pipelines take 5–30s and the HTTP share request must return
// immediately. stdout/stderr are discarded; the score path writes to the
// queue + sidecar + FCM on its own.
func (r *Router) handleInlineTriage(ac *ActionConfig, command string) (string, error) {
	shell := r.tmux.shell()
	shellArg := r.tmux.shellArgs()
	slog.Debug("inline triage",
		"event_type", "inline_triage",
		"action", ac.ID,
		"shell", shell,
		"command", command,
	)
	cmd := exec.Command(shell, shellArg, command)
	// Detach from parent stdio so the server doesn't block on the child.
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("inline triage exec: %w", err)
	}
	// Fire and forget: reap the child in the background to avoid zombies.
	go func() { _ = cmd.Wait() }()
	return "Scoring headless — verdict via FCM", nil
}

func (r *Router) handleRegex(ac *ActionConfig, req *ShareRequest) (string, error) {
	// Try text first, then URL.
	match := ac.ExtractMatch(strings.TrimSpace(req.Text))
	if match == "" {
		match = ac.ExtractMatch(req.URL)
	}
	if match == "" {
		return "", fmt.Errorf("no match for pattern in action %q", ac.ID)
	}

	session := strings.Split(req.Target, ":")[0]
	data := TemplateData{
		URL:     req.URL,
		Text:    req.Text,
		Profile: req.Profile,
		Match:   match,
	}

	command, err := ac.RenderCommand(data)
	if err != nil {
		return "", err
	}

	if err := r.tmux.NewWindow(session, command, match); err != nil {
		return "", err
	}
	return fmt.Sprintf("Spinning up %s", match), nil
}

// shellQuote wraps a string in single quotes, escaping embedded single quotes.
// This prevents shell injection via URL payloads.
func shellQuote(s string) string {
	escaped := strings.ReplaceAll(s, "'", "'\\''")
	return "'" + escaped + "'"
}

// urlToSlug extracts a short slug from a URL for use as a tmux window name.
func urlToSlug(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Path == "" || u.Path == "/" {
		if u != nil && u.Host != "" {
			return sanitizeWindowName(u.Host)
		}
		return "untitled"
	}
	path := strings.Trim(u.Path, "/")
	slug := strings.ReplaceAll(path, "/", "-")
	return sanitizeWindowName(slug)
}

// sanitizeWindowName removes characters that are problematic in tmux window names.
func sanitizeWindowName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == ':' || r == '.' || r < 32:
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) > 50 {
		s = s[:50]
	}
	return s
}
