package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

// ErrActionNotFound is returned by Route when the resolved action ID has no
// ActionConfig entry. This is a permanent config gap, not a transient tmux
// failure, so callers should return 4xx rather than queuing for replay.
var ErrActionNotFound = errors.New("action not found")

// archiveThresholdCache lazily loads the actions config on first use and
// supports hot-reload via SIGHUP (EPIC-051 M6). The sync.Once pattern that
// preceded this holder required a full server restart to pick up a
// threshold change  -  the 15-minute diagnostic detour documented in the
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
	loaded, err := LoadConfig(context.Background(), "")
	if err != nil {
		loaded = builtinConfig()
	}
	archiveThresholdCfg = loaded
	return archiveThresholdCfg
}

// ReloadArchiveThresholdConfig re-parses config.toml and atomically swaps
// the cached config. Safe to call from a signal handler  -  the write is
// guarded by the same mutex readers use.  EPIC-051 M6.
func ReloadArchiveThresholdConfig() error {
	loaded, err := LoadConfig(context.Background(), "")
	if err != nil {
		return fmt.Errorf("reload archive threshold: %w", err)
	}
	archiveThresholdMu.Lock()
	archiveThresholdCfg = loaded
	archiveThresholdMu.Unlock()
	slog.Info(
		"archive threshold config reloaded",
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
	tmux           *TmuxRunner
	actions        []Action
	actionsCfg     []ActionConfig
	cfgIndex       map[string]*ActionConfig
	debug          bool
	mu             sync.RWMutex
	queue          *Queue
	bskyClient     *BlueskyClient     // EPIC-094: threaded for scoreAsync verdict replies
	whisperModel   string             // EPIC-067: path to ggml model file for audio transcription // EPIC-060: for server-side scoring goroutine
	ytdlpPath      string             // EPIC-009: path to yt-dlp binary for YouTube transcription
	events         *EventLogger       // EPIC-076: classification telemetry; nil when event logging not configured
	serverConfig   *ServerConfig      // EPIC-098 F3: server config for YouTube sub-behavior toggles
	wikiResolver   *WikiTopicResolver // EPIC-180 M2: nil when wiki is disabled or vault missing
	domainRouter   *DomainRouter      // EPIC-258 M2: was package var pkgDomainRouter
	jina           *jinaClient        // EPIC-258 M2: was package vars jinaBaseURL/jinaHTTPClient; nil = production client
	ytDeps         *ytDeps            // EPIC-258 M2: was package var execYtdlp; nil = production seams
	scoringBackend ScoringBackend     // EPIC-258 M2: nil = process default (activeScoringBackend)
	scoringDepsMut func(*scoringDeps) // EPIC-258 M2: test-only mutator applied to freshly built scoringDeps; nil = no-op
}

// SetQueue wires the queue for server-side uinit_* scoring (EPIC-060 M1).
// Called by NewServer so scoreURLAsync goroutines can persist results.
func (r *Router) SetQueue(q *Queue) {
	r.mu.Lock()
	r.queue = q
	r.mu.Unlock()
}

// SetBskyClient wires the Bluesky client for verdict reply publishing (EPIC-094).
func (r *Router) SetBskyClient(c *BlueskyClient) {
	r.mu.Lock()
	r.bskyClient = c
	r.mu.Unlock()
}

// SetWhisperModel sets the whisper model path for audio transcription (EPIC-067).
func (r *Router) SetWhisperModel(path string) {
	r.mu.Lock()
	r.whisperModel = path
	r.mu.Unlock()
}

// SetYtdlpPath sets the yt-dlp binary path for YouTube transcription (EPIC-009 M2).
func (r *Router) SetYtdlpPath(path string) {
	r.mu.Lock()
	r.ytdlpPath = path
	r.mu.Unlock()
}

// SetEvents wires the EventLogger for classification telemetry (EPIC-076 M1).
// Called after EventLogger creation in main.go so classify_stage_win events
// can be emitted from scoreURLAsync and scoreFileAsync goroutines.
func (r *Router) SetEvents(e *EventLogger) {
	r.mu.Lock()
	r.events = e
	r.mu.Unlock()
}

// SetServerConfig wires the server config for YouTube sub-behavior toggles (EPIC-098 F3).
// Called during server initialization in main.go.
func (r *Router) SetServerConfig(cfg *ServerConfig) {
	r.mu.Lock()
	r.serverConfig = cfg
	r.mu.Unlock()
}

// SetDomainRouter installs the domain router for URL fetch routing (EPIC-010 M5).
// Called after router construction in main.go during server init.
//
// EPIC-258 M2: stores the router on the Router instance instead of a package
// var. The previous package-level installation was read concurrently by
// scoring goroutines and was the production data race reported by -race.
func (r *Router) SetDomainRouter(dr *DomainRouter) {
	r.mu.Lock()
	r.domainRouter = dr
	r.mu.Unlock()
}

// scoringDepsFn returns a lazy resolver for this Server's scoring
// dependencies. EPIC-258 M2: resolution must be deferred until use, because
// sources are registered before main.go installs the domain router.
// Safe on a nil Server or nil Router  -  falls back to production defaults.
func (s *Server) scoringDepsFn() func() *scoringDeps {
	return func() *scoringDeps {
		if s == nil || s.router == nil {
			return newScoringDeps(nil, nil)
		}
		return s.router.scoringDeps()
	}
}

// SetJinaClient overrides the Jina Reader client for URL fetches. Used by
// tests to point fetches at a local httptest.Server. nil restores the
// production client. EPIC-258 M2.
func (r *Router) SetJinaClient(j *jinaClient) {
	r.mu.Lock()
	r.jina = j
	r.mu.Unlock()
}

// SetYtDeps overrides the YouTube pipeline's execution seams. Used by tests
// to inject a yt-dlp stub. nil restores production behaviour. EPIC-258 M2.
func (r *Router) SetYtDeps(d *ytDeps) {
	r.mu.Lock()
	r.ytDeps = d
	r.mu.Unlock()
}

// youtubeDeps returns this Router's YouTube seams.
func (r *Router) youtubeDeps() *ytDeps {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ytDeps
}

// scoringDeps builds the scoring dependency set for this Router.
func (r *Router) scoringDeps() *scoringDeps {
	r.mu.RLock()
	cfg := r.serverConfig
	dr := r.domainRouter
	j := r.jina
	r.mu.RUnlock()
	d := newScoringDeps(cfg, dr)
	if j != nil {
		d.Jina = j
	}
	r.mu.RLock()
	d.Backend = r.scoringBackend
	mut := r.scoringDepsMut
	r.mu.RUnlock()
	if mut != nil {
		mut(d)
	}
	return d
}

// SetScoringBackend overrides the scoring backend used by goroutines this
// Router launches. Used by tests to inject a deterministic fake instead of
// swapping the activeScoringBackend package var, which scoring goroutines
// read concurrently (EPIC-258 M2). nil restores the process default.
func (r *Router) SetScoringBackend(b ScoringBackend) {
	r.mu.Lock()
	r.scoringBackend = b
	r.mu.Unlock()
}

// SetScoringDepsMutator installs a test-only mutator applied to every
// scoringDeps this Router builds, so tests can inject exec stubs (ffmpeg,
// whisper, ...) into router-launched scoring goroutines without package-var
// swaps (EPIC-258 M2). nil removes the mutator.
func (r *Router) SetScoringDepsMutator(mut func(*scoringDeps)) {
	r.mu.Lock()
	r.scoringDepsMut = mut
	r.mu.Unlock()
}

// SetWikiResolver wires the wiki topic resolver for wiki-context scoring.
// Called after server config load in main.go; nil disables wiki context injection.
func (r *Router) SetWikiResolver(wr *WikiTopicResolver) {
	r.mu.Lock()
	r.wikiResolver = wr
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
	slog.Debug(
		"router reloaded",
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
// using this router's live actions config. With EPIC-061 auto-profile, the
// single uinit_auto action's threshold applies to all profiles. Falls back
// to the package-level archiveThreshold helper when no match is found.
func (r *Router) ArchiveThreshold(profile string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, a := range r.actionsCfg {
		switch a.ProfileMap {
		case "auto":
			if strings.HasPrefix(a.ID, "uinit_") && a.ArchiveThreshold != 0 {
				return a.ArchiveThreshold
			}
		case "prefix":
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
// fallback). EPIC-061: checks uinit_auto (ProfileMap=auto) first, then
// falls back to prefix-based lookup for user overrides, then default.
func archiveThreshold(profile string) int {
	cfg := loadArchiveThresholdConfig()
	for i := range cfg.Actions {
		a := &cfg.Actions[i]
		if a.ProfileMap == "auto" && strings.HasPrefix(a.ID, "uinit_") && a.ArchiveThreshold != 0 {
			return a.ArchiveThreshold
		}
		if a.ProfileMap == "prefix" && strings.TrimPrefix(a.ID, "uinit_") == profile {
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
// that made the override (e.g. "jira_auto_route", "domain_heuristic").
type ShareResolution struct {
	ReceivedAction  string
	ReceivedProfile string
	ResolvedAction  string
	ResolvedProfile string
	Reason          string
	// EPIC-156 F3: intent-based resolution fields (added alongside soak compat).
	ResolvedIntent string   // "score" | "capture" | "transcribe"; empty until F3 is fully wired
	InferredTags   []string // system-inferred tags from cascade; nil until F3 is fully wired
	ClassifySource string   // "caller" | "domain_heuristic" | "content" | "default"
}

// jiraURLRE matches Jira-hosted issue URLs (atlassian.net/browse/KEY-123 or
// jira.*/browse/KEY-123). Used by resolveShareAction for auto-routing
// uinit_auto → ginit_auto on Jira URLs.
var jiraURLRE = regexp.MustCompile(`(?i)(?:atlassian\.net|jira\.[^/]+)/browse/[A-Z][A-Z0-9]+-\d+`)

// domainProfileMap maps URL domain substrings to profiles. Checked in order
// by classifyURLProfile; first match wins. Extend this table when adding new
// profile heuristics.
var domainProfileMap = []struct {
	substr  string
	profile string
}{
	// eng
	{"atlassian.net", "eng"}, // Jira issues + Confluence pages
	{"github.com", "eng"},
	{"gitlab.com", "eng"},
	{"stackoverflow.com", "eng"},
	{"stackexchange.com", "eng"},
	{"arxiv.org", "eng"},
	{"hacker-news", "eng"},
	{"news.ycombinator.com", "eng"},
	{"dev.to", "eng"},
	{"medium.com", "eng"},
	// travel
	{"booking.com", "travel"},
	{"airbnb.com", "travel"},
	{"tripadvisor.com", "travel"},
	{"expedia.com", "travel"},
	{"kayak.com", "travel"},
	{"tourismboard", "travel"},
	{"travel", "travel"},
	// life
	{"retirement", "life"},
	// music  -  spotify.com and soundcloud.com are also matched by unsupportedPipelineRE
	// and pre-filtered before scoring. Their entries here ensure profile="music" is
	// assigned on the inbound request path (used for logging/events), even though
	// scoring is skipped. EPIC-088 M4: retained as profile-classification escape hatch.
	{"spotify.com", "music"},
	{"soundcloud.com", "music"},
	{"bandcamp.com", "music"},
	{"music.apple.com", "music"},
	// finance
	{"bloomberg.com", "finance"},
	{"reuters.com", "finance"},
	{"wsj.com", "finance"},
	{"finance.yahoo.com", "finance"},
	{"cnbc.com", "finance"},
	// dining
	{"yelp.com", "dining"},
	{"opentable.com", "dining"},
	{"doordash.com", "dining"},
	{"ubereats.com", "dining"},
	{"grubhub.com", "dining"},
	{"nytimes.com/section/food", "dining"},
	// fashion
	{"zara.com", "fashion"},
	{"hm.com", "fashion"},
	{"asos.com", "fashion"},
	{"net-a-porter.com", "fashion"},
	{"vogue.com", "fashion"},
	// finance (investor relations subdomains  -  EPIC-087 M3)
	{"ir.", "finance"},
	{"investor.", "finance"},
	{"investors.", "finance"},
	// life  -  privacy/legal content (EPIC-087 M3)
	{"globalprivacycontrol", "life"},
	{"privacyrights", "life"},
	{"privacypolicy", "life"},
	{"terms-of-service", "life"},
	{"termsofservice", "life"},
}

// classifyURLProfile returns the heuristic profile for a URL based on domain
// matching. The second return value indicates whether a positive domain match
// was found. When matched is false the "eng" fallback is returned but should
// not be treated as authoritative  -  callers may use content-based classification
// to refine the profile.
func classifyURLProfile(rawURL string) (string, bool) {
	lower := strings.ToLower(rawURL)
	for _, dm := range domainProfileMap {
		if strings.Contains(lower, dm.substr) {
			return dm.profile, true
		}
	}
	return "eng", false // fallback  -  not a positive match
}

// resolveShareAction is the single chokepoint every queue-writing path goes
// through to determine the final (action, profile) pair for a share request.
// It enforces EPIC-052's caller-wins invariant: when received.Action is present
// in cfgIndex, the received action is preserved verbatim. Unknown or missing
// actions are returned as-is; Route fails fast on lookup miss.
//
// EPIC-061 M2: when heuristicOverrideEnabled is true and the action has
// ProfileMap="auto", domain heuristics classify the URL into a profile and
// Jira URLs are transparently rerouted from uinit_auto to ginit_auto.
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

		// EPIC-061 M2: auto-profile heuristics for ProfileMap="auto".
		// Note: F1 replaced routeJiraURL with resolveDomainRoute (domain_route.go).
		// resolveDomainRoute fires before ResolveShare in handleShare.
		// resolveShareAction does not perform domain routing.
		if ac.ProfileMap == "auto" && heuristicOverrideEnabled {
			// Domain heuristic profile classification.
			if profile == "" {
				classified, matched := classifyURLProfile(req.URL)
				profile = classified
				if !matched {
					res.Reason = "domain_fallback"
				}
			}
		}

		res.ResolvedAction = actionID
		res.ResolvedProfile = profile
		return res
	}

	// Unknown / missing action  -  try bare-intent normalization before failing fast.
	// If caller sent a bare intent keyword (e.g. "uinit") without a profile suffix,
	// attempt to resolve to the "_auto" variant (e.g. "uinit_auto"). This guards
	// against Android clients that derive the action field from the intent name
	// rather than the fully-qualified ActionConfig.ID. (PA-3: uinit-action-unresolved)
	if autoID := actionID + "_auto"; cfgIndex[autoID] != nil {
		ac := cfgIndex[autoID]
		if ac.ProfileMap == "prefix" && profile == "" {
			if parts := strings.SplitN(autoID, "_", 2); len(parts) == 2 {
				profile = parts[1]
			}
		}
		res.ResolvedAction = autoID
		res.ResolvedProfile = profile
		res.ResolvedIntent = req.Intent
		res.ClassifySource = req.ClassifySource
		res.Reason = "bare_action_normalized"
		return res
	}

	// Fallback: return as-is and let Route fail fast.
	res.ResolvedAction = actionID
	res.ResolvedProfile = profile

	// EPIC-156 F3: populate intent fields from caller-supplied values.
	// Full 6-stage cascade is Phase 2; during soak the intent is already set by handleShare
	// via F1/F8 derivation before ResolveShare is called.
	res.ResolvedIntent = req.Intent
	res.ClassifySource = req.ClassifySource

	return res
}

// checkAuthScopeIntent checks whether the intent+tag combination is permitted for the bearer token.
// Replaces action-ID-prefix scope check in Phase 2. During soak, runs alongside checkScopedAuth.
// EPIC-156 F3.
func checkAuthScopeIntent(intent string, userTags []string, isMobileToken, isJiraToken bool) error {
	if intent != "capture" {
		return nil // score and transcribe require only standard bearer token
	}
	for _, tag := range userTags {
		switch tag {
		case "jira":
			if !isJiraToken {
				return fmt.Errorf("scope_violation_capture_jira: capture+jira requires jira_token")
			}
		case "confluence":
			if !isMobileToken && !isJiraToken {
				return fmt.Errorf("scope_violation_capture_confluence: capture+confluence requires confluence_token")
			}
		case "github":
			if !isMobileToken && !isJiraToken {
				return fmt.Errorf("scope_violation_capture_github: capture+github requires github_token")
			}
		}
	}
	return nil
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
		return "", fmt.Errorf("no action for %q: %w", actionID, ErrActionNotFound)
	}

	slog.Debug(
		"route decision",
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

	// EPIC-090 M1: vnote_auto + YouTube URL routes to transcribeYouTubeAsync
	// (transcript-only, no scoring). Must come before the generic YouTube check
	// so vnote_auto shares don't fall through to scoreYouTubeAsync.
	if ac.ServerScore && ac.ID == "vnote_auto" && isYouTubeURL(req.URL) {
		ytPath := r.ytdlpPath
		if ytPath == "" {
			ytPath = ytdlpBinaryPath
		}
		go transcribeYouTubeAsync(*req, r.queue, ytPath, r.events, r.whisperModel, r.serverConfig, r.youtubeDeps())
		return "Fetching YouTube transcript  -  ready via FCM", nil
	}

	// EPIC-009 M4: YouTube URL shares route to scoreYouTubeAsync for yt-dlp
	// transcription and Claude scoring. Must come before the audio branch.
	// EPIC-003 M3: also route when req.Type=""  -  Android/Chrome clients may omit type.
	if ac.ServerScore && (req.Type == "url" || req.Type == "") && isYouTubeURL(req.URL) {
		ytPath := r.ytdlpPath
		if ytPath == "" {
			ytPath = ytdlpBinaryPath
		}
		go scoreYouTubeAsync(*req, r.queue, ytPath, r.events, r.whisperModel, r.serverConfig, r.youtubeDeps())
		return "Transcribing YouTube  -  verdict via FCM", nil
	}

	// EPIC-077 M5: audio shares route to processVoiceNoteAsync (renamed from
	// scoreAudioAsync). Architecturally incompatible with scoreAsync  -
	// hardcoded score=100, execHaiku directly, 1800s timeout, transcript management.
	if ac.ServerScore && req.Type == "audio" {
		deps := r.scoringDeps()
		go processVoiceNoteAsync(req.AudioPath, req.Profile, r.queue, req.QueueRowID, req.OriginalFilename, r.whisperModel, req.ExtraText, req, r.events, HaikuJSONEvaluator{Backend: deps.Backend}, deps)
		return "Transcribing  -  synopsis via FCM", nil
	}

	// image, document, and URL shares all route to scoreAsync.
	// scoreAsync branches on req.Type for content acquisition:
	//   - "url": Jina fetch (or screenshot metadata)
	//   - "document": lit parse text extraction, metadata fallback
	//   - "image": metadata synthesis
	if ac.ServerScore && (req.Type == "image" || req.Type == "document" || req.Type == "url" || req.Type == "") {
		deps := r.scoringDeps()
		go scoreAsync(req, r.queue, HaikuJSONEvaluator{Backend: deps.Backend}, r.events, r.bskyClient, r.wikiResolver, deps)
		switch req.Type {
		case "image", "document":
			return "Scoring file  -  verdict via FCM", nil
		default:
			return "Scoring  -  verdict via FCM", nil
		}
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
	slog.Debug(
		"inline triage",
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
	return "Scoring headless  -  verdict via FCM", nil
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
