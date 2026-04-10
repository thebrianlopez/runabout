package main

import (
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"strings"
	"sync"
)

// archiveThresholdCache lazily loads the actions config on first use and
// supports hot-reload via SIGHUP (EPIC-051 M6). The sync.Once pattern that
// preceded this holder required a full server restart to pick up a
// threshold change — the 15-minute diagnostic detour documented in the
// EPIC-050 PoMo timeline. Now a `kill -HUP $(cat linkari.pid)` is enough.
var (
	archiveThresholdMu  sync.RWMutex
	archiveThresholdCfg *Config
)

func loadArchiveThresholdConfig() *Config {
	archiveThresholdMu.RLock()
	cfg := archiveThresholdCfg
	archiveThresholdMu.RUnlock()
	if cfg != nil {
		return cfg
	}

	// Slow path: first call since process start (or since an explicit reset).
	// Upgrade to a write lock and double-check.
	archiveThresholdMu.Lock()
	defer archiveThresholdMu.Unlock()
	if archiveThresholdCfg != nil {
		return archiveThresholdCfg
	}
	loaded, err := LoadConfig("")
	if err != nil {
		loaded = builtinConfig()
	}
	archiveThresholdCfg = loaded
	return archiveThresholdCfg
}

// ReloadArchiveThresholdConfig re-parses actions.yaml and atomically swaps
// the cached config. Safe to call from a signal handler — the write is
// guarded by the same mutex readers use.  EPIC-051 M6.
func ReloadArchiveThresholdConfig() error {
	loaded, err := LoadConfig("")
	if err != nil {
		return fmt.Errorf("reload archive threshold: %w", err)
	}
	archiveThresholdMu.Lock()
	archiveThresholdCfg = loaded
	archiveThresholdMu.Unlock()
	slog.Info("archive threshold config reloaded",
		"event_type", "archive_threshold_reloaded",
		"action_count", len(loaded.Actions),
	)
	return nil
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
	queue      *Queue // EPIC-060: for server-side scoring goroutine
}

// SetQueue wires the queue for server-side uinit_* scoring (EPIC-060 M1).
// Called by NewServer so scoreURLAsync goroutines can persist results.
func (r *Router) SetQueue(q *Queue) {
	r.mu.Lock()
	r.queue = q
	r.mu.Unlock()
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

// ResolveShare is the server-facing entry point for EPIC-052's provenance
// helper. It takes a read lock on the router's cfgIndex and delegates to the
// pure `resolveShareAction` helper. Call this BEFORE writing a queue row so
// the share_action_resolved event emitted by handleShare reflects the same
// resolution the router will apply during Route.
func (r *Router) ResolveShare(req *ShareRequest, heuristicOverrideEnabled bool) ShareResolution {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return resolveShareAction(req, r.cfgIndex, heuristicOverrideEnabled)
}

// LookupAction returns the ActionConfig for a given action ID, or nil if not found.
func (r *Router) LookupAction(actionID string) *ActionConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfgIndex[actionID]
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

// ShareResolution records the provenance of a share request's (action, profile)
// resolution. EPIC-052: every ingress path that writes a queue row emits this
// so that a later queue-row inspection can be reconciled against what the
// caller actually sent. Reason is empty when the resolved value matches the
// received value (i.e. the caller-wins default held). Reason is non-empty
// whenever the helper rewrote either field; the string names the code site
// that made the override (e.g. "bare_uinit_pinned:uinit_eng",
// "unknown_uinit_profile_pinned:uinit_eng").
type ShareResolution struct {
	ReceivedAction  string
	ReceivedProfile string
	ResolvedAction  string
	ResolvedProfile string
	Reason          string
}

// resolveShareAction is the single chokepoint every queue-writing path goes
// through to determine the final (action, profile) pair for a share request.
// It enforces EPIC-052's caller-wins invariant: when received.Action is present
// in cfgIndex, the received action is preserved verbatim. Unknown or missing
// actions are returned as-is; Route fails fast on lookup miss.
//
// EPIC-060 M2: branches 1–3 (empty-action fallback, bare-"uinit" pin,
// unknown-"uinit_<profile>" pin) removed. All uinit_* actions are now
// registered in cfgIndex as ServerScore=true and always hit the caller-wins
// branch directly. The three legacy fallback paths produced tmux-routable
// action IDs; that routing no longer exists.
//
// The helper is pure (no IO, no logging) so unit tests exercise it directly.
func resolveShareAction(req *ShareRequest, cfgIndex map[string]*ActionConfig, heuristicOverrideEnabled bool) ShareResolution {
	res := ShareResolution{
		ReceivedAction:  req.Action,
		ReceivedProfile: req.Profile,
	}

	actionID := req.Action
	profile := req.Profile

	// Caller-wins: known action is returned unchanged.
	if ac, ok := cfgIndex[actionID]; ok {
		if ac.ProfileMap == "prefix" && profile == "" {
			parts := strings.SplitN(actionID, "_", 2)
			if len(parts) == 2 {
				profile = parts[1]
			}
		}
		res.ResolvedAction = actionID
		res.ResolvedProfile = profile
		// heuristicOverrideEnabled is a no-op: no heuristic is registered today.
		_ = heuristicOverrideEnabled
		return res
	}

	// Unknown / missing action — return as-is and let Route fail fast.
	res.ResolvedAction = actionID
	res.ResolvedProfile = profile
	return res
}

// Route dispatches a request using config-driven actions.
func (r *Router) Route(req *ShareRequest) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// EPIC-052: resolveShareAction is the single resolver for (action, profile).
	// It replaces the non-deterministic map-iteration fallback that previously
	// lived here (Footgun 3 in the EPIC-050 PoMo). The helper is pure; the
	// caller-wins invariant + event emission happen in handleShare before the
	// queue row is written.
	resolution := resolveShareAction(req, r.cfgIndex, false)
	actionID := resolution.ResolvedAction
	req.Profile = resolution.ResolvedProfile

	// Resolve target from action definition when the client doesn't send one.
	if req.Target == "" && actionID != "" {
		if ac, ok := r.cfgIndex[actionID]; ok {
			req.Target = ac.Target
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

	// EPIC-060 M1: server_score=true runs the full scoring pipeline entirely
	// server-side — no tmux window, no shell subprocess. A goroutine fetches
	// page content via Jina Reader, evaluates via Haiku, and persists through
	// Queue.ScoreByURL + EnqueueDigestIfDue. Returns immediately; verdict
	// arrives via FCM push.
	if ac.ServerScore {
		go scoreURLAsync(req.URL, req.Profile, r.queue, HaikuMarkdownEvaluator{})
		return "Scoring — verdict via FCM", nil
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
