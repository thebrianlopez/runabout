package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/blo-grindr/runabout/internal/secrets"
)

// PushConfig holds the runtime knobs used by Queue.EnqueueDigestIfDue.
// EPIC-051 M2: consolidates the per-profile throttle, the global default, and
// the NotifyMinScore floor into a single value object owned by Queue.
type PushConfig struct {
	// DigestThrottle is a per-profile override map. Profile name → min time
	// between digest pushes for that profile. A missing key falls back to
	// DigestThrottleDefault.
	DigestThrottle map[string]time.Duration
	// DigestThrottleDefault is the fallback throttle window for profiles not
	// listed in DigestThrottle. Zero means "use the hardcoded 1h default".
	DigestThrottleDefault time.Duration
	// NotifyMinScore is a uniform score floor applied across all paths.
	// 0 disables the floor. EPIC-051 M1 decision: honor as a global gate.
	NotifyMinScore int
}

// resolvePushConfigOnce loads ~/.config/linkari/config.toml (if present) and
// installs its push settings on the given queue. Used by the CLI score path
// so `linkari score` honors the operator's configured throttles / min-score
// floor without booting a server. Errors are swallowed intentionally — the
// CLI should fall back to zero-value defaults rather than refuse to score.
func resolvePushConfigOnce(q *Queue) {
	if q == nil {
		return
	}
	cfg, err := LoadConfig(context.Background(), "")
	if err != nil || cfg == nil {
		q.SetPushConfig(&PushConfig{})
		return
	}
	q.SetPushConfig(cfg.Server.PushConfig())
}

// IsZero reports whether the ServerConfig has any non-default fields set.
// Used as a "was a [server:] block present?" check in main.go now that the
// struct contains non-comparable fields (PushYAMLConfig holds maps).
func (s ServerConfig) IsZero() bool {
	return s.Port == 0 && s.Token == "" && s.QueueDB == "" && s.FirebaseSA == "" &&
		s.LogFile == "" && s.Shell == "" && s.ShellArgs == "" && s.NotifyMinScore == 0 &&
		s.ServerURL == "" && s.TSNetAuthKey == "" && s.Tsnet == nil &&
		s.TsnetHostname == "" && s.TsnetStateDir == "" && !s.Debug &&
		s.JiraAPIUsername == "" && s.JiraAPIPassword == "" && s.JiraDomain == "" && s.PagerDutyToken == "" &&
		len(s.Push.DigestThrottle) == 0 && s.Push.DigestThrottleDefault.D == 0 &&
		s.RelayedWatchdogInterval.D == 0 && s.RelayedWatchdogMaxAge.D == 0 &&
		s.SnapshotInterval.D == 0 && s.SnapshotPath == "" &&
		!s.Share.HeuristicOverrideEnabled &&
		s.Shield.Mode == "" &&
		s.WhisperModel == "" && s.FfmpegPath == "" &&
		s.TranscriptsDir == "" && s.YtdlpPath == "" &&
		s.YouTube.SubtitleLangs == "" && s.YouTube.TimeoutSeconds == 0 &&
		s.GoogleClientID == "" && s.SessionTTLDays == 0 &&
		!s.Sandbox.Enabled &&
		s.ImageNoiseGateMaxBytes == 0 && s.MaxScoringCostUSD == 0 &&
		s.LiteParseePath == ""
}

// YouTubeConfig holds per-field tuning for yt-dlp extraction. EPIC-090 M5.
type YouTubeConfig struct {
	SubtitleLangs   string `toml:"subtitle_langs"`    // yt-dlp --sub-langs value (default: "en.*,en")
	TimeoutSeconds  int    `toml:"timeout_seconds"`   // extraction timeout in seconds (default: 30)
	FallbackToAudio bool   `toml:"fallback_to_audio"` // EPIC-001 M3: download audio + whisper when no subtitles (default: true via package var)
}

// RelayedWatchdogConfig is the resolved runtime view of the watchdog knobs,
// with defaults filled in. Returns (0, 0) iff the watchdog is explicitly
// disabled (max age <= 0 after defaults).
//
// EPIC-054 M3: 60s interval, 15m (900s) max age chosen so a scoring pipeline
// stuck in `relayed` is reclassified within one tick of the 15-minute budget
// — long enough to absorb a slow-but-working score run, short enough to keep
// orphans from accumulating across the overnight window where the epic was
// discovered.
//
// EPIC-055 M1/M3: UrlWorkDir enables the on-disk rescue path; AlertThreshold
// and AlertWindow enable the volume-based alert. Zero values disable each feature.
type RelayedWatchdogCfg struct {
	Interval time.Duration
	MaxAge   time.Duration
	// UrlWorkDir is the root dir scanned for _score.json rescue files.
	// Default: $LINKARI_URL_WORK_DIR or $HOME/code/personal/url_work.
	// Empty string disables the rescue path (Tier-1 rollback knob).
	UrlWorkDir string
	// AlertThreshold, when > 0, triggers an FCM + JSONL alert when the number
	// of share_scoring_timeout events within AlertWindow exceeds this value.
	// 0 disables alerting (Tier-1 rollback knob). EPIC-055 M3.
	AlertThreshold int
	// AlertWindow is the rolling window for AlertThreshold counting.
	// Default: 10 minutes. EPIC-055 M3.
	AlertWindow time.Duration
}

// RelayedWatchdog returns the resolved watchdog config from a ServerConfig.
// Missing/zero values fall back to the 60s/900s defaults. A negative max age
// explicitly disables the watchdog (returns a zero-valued struct).
func (s *ServerConfig) RelayedWatchdog() RelayedWatchdogCfg {
	iv := s.RelayedWatchdogInterval.Duration()
	ma := s.RelayedWatchdogMaxAge.Duration()
	if iv == 0 {
		iv = 60 * time.Second
	}
	if ma == 0 {
		ma = 15 * time.Minute
	}
	if ma < 0 {
		return RelayedWatchdogCfg{}
	}

	urlWorkDir := s.RelayedWatchdogUrlWorkDir
	if urlWorkDir == "" {
		if v := os.Getenv("LINKARI_URL_WORK_DIR"); v != "" {
			urlWorkDir = v
		} else {
			home, _ := os.UserHomeDir()
			urlWorkDir = filepath.Join(home, "code", "personal", "url_work")
		}
	}

	alertWindow := s.RelayedWatchdogAlertWindow.Duration()
	if alertWindow == 0 {
		alertWindow = 10 * time.Minute
	}

	return RelayedWatchdogCfg{
		Interval:       iv,
		MaxAge:         ma,
		UrlWorkDir:     urlWorkDir,
		AlertThreshold: s.RelayedWatchdogAlertThreshold,
		AlertWindow:    alertWindow,
	}
}

// SnapshotConfig returns the interval and destination path for the snapshot
// worker. Defaults: 1h interval, <queue_db_path>.bak. A negative interval
// disables the worker (returns interval=0, destPath="").
func (s *ServerConfig) SnapshotConfig(queueDBPath string) (interval time.Duration, destPath string) {
	iv := s.SnapshotInterval.Duration()
	if iv < 0 {
		return 0, ""
	}
	if iv == 0 {
		iv = time.Hour
	}
	destPath = s.SnapshotPath
	if destPath == "" {
		destPath = queueDBPath + ".bak"
	}
	return iv, destPath
}

// PushConfig derives a *PushConfig from the ServerConfig fields relevant to
// push gating. EPIC-051 M4 extends ServerConfig with a push subconfig; this
// method is the single resolution seam both server and CLI paths go through.
func (s *ServerConfig) PushConfig() *PushConfig {
	return &PushConfig{
		NotifyMinScore:        s.NotifyMinScore,
		DigestThrottle:        s.Push.DigestThrottle.Durations(),
		DigestThrottleDefault: s.Push.DigestThrottleDefault.Duration(),
	}
}

// ThrottleFor returns the effective throttle window for the given profile.
// Missing profile → DigestThrottleDefault; missing default → 1 hour.
func (p *PushConfig) ThrottleFor(profile string) time.Duration {
	if p == nil {
		return time.Hour
	}
	if p.DigestThrottle != nil {
		if d, ok := p.DigestThrottle[profile]; ok && d > 0 {
			return d
		}
	}
	if p.DigestThrottleDefault > 0 {
		return p.DigestThrottleDefault
	}
	return time.Hour
}

// DomainRoute maps a URL substring pattern to an override action ID.
// When a share request's URL contains Pattern, req.Action is replaced with
// OverrideAction before scoped-auth and scoring. First-match wins.
// Configured via the top-level `domain_routes:` block in config.toml (F1).
type DomainRoute struct {
	Pattern        string `toml:"pattern"`
	OverrideAction string `toml:"override_action"`
}

// ActionKind determines how a share request is dispatched.
type ActionKind string

const (
	// KindTemplate renders a Go text/template with request fields.
	KindTemplate ActionKind = "template"
	// KindLiteral pastes text literally into a tmux target (no execution).
	KindLiteral ActionKind = "literal"
	// KindRegex extracts a match from text/URL before rendering a command template.
	KindRegex ActionKind = "regex"
	// KindCapture routes to the structured-content capture pipeline (F2).
	// Not yet implemented: falls through to scoreAsync until F2 lands.
	KindCapture ActionKind = "capture"
)

// ActionConfig defines a single share action in the TOML config.
type ActionConfig struct {
	ID               string     `toml:"id"`
	Label            string     `toml:"label"`
	Icon             string     `toml:"icon"`
	Type             string     `toml:"type"`             // "url" or "text"
	Target           string     `toml:"target"`           // tmux target "session:pane"
	Kind             ActionKind `toml:"kind"`             // template, literal, regex
	CommandTemplate  string     `toml:"command_template"` // Go text/template string
	Pattern          string     `toml:"pattern"`          // regex for kind=regex
	ArchiveThreshold int        `toml:"archive_threshold"` // -1 = no auto-archive
	ProfileMap       string     `toml:"profile_map"`       // "prefix" = extract from id prefix; "auto" = server-side heuristic (EPIC-061)
	Condition        string     `toml:"condition"`         // "env:VAR=VALUE" — only register when condition met
	InlineTriage        bool `toml:"inline_triage"`        // EPIC-043 M5: run command headlessly, skip tmux window (fire-and-forget)
	AutoScore           bool `toml:"auto_score"`           // EPIC-057: enqueue as scored immediately (skip watchdog)
	ConfidenceThreshold int  `toml:"confidence_threshold"` // EPIC-058 M3: minimum score to pass confidence gate (0 = no gate)
	AutoLaunch          bool `toml:"auto_launch"`          // EPIC-058 M3: auto-launch ginit when gate passes (requires confidence_threshold > 0)
	ServerScore         bool `toml:"server_score"`         // EPIC-060: score uinit_* actions server-side via Jina+Haiku (no tmux window)
	ForceContentClassify bool `toml:"force_content_classify"` // EPIC-084 M3: always run content-LLM classification even when cascade produces a profile
	ShortsRubricTemplate string `toml:"shorts_rubric_template"` // EPIC-012 M7: rubric override for YouTube Shorts scoring

	// F2: KindCapture fields — required when kind=capture.
	ArtifactDir              string `toml:"artifact_dir"`               // base dir, e.g. "docs/captures"
	ArtifactFilenameTemplate string `toml:"artifact_filename_template"` // Go tmpl, e.g. "{{.Date}}_{{.Key}}.md"
	PostCaptureCommand       string `toml:"post_capture_command"`       // F5 hook point (no-op until F5 TDD)

	// Parsed fields (not in TOML)
	compiledTemplate            *template.Template
	compiledRegex               *regexp.Regexp
	compiledPostCaptureTemplate *template.Template // F5: compiled from PostCaptureCommand
}

// PostCaptureContext is the template data available to PostCaptureCommand.
type PostCaptureContext struct {
	ArtifactPath string
	Key          string
	URL          string
	Date         string
}

// Config is the top-level TOML config file.
type Config struct {
	DefaultArchiveThreshold int            `toml:"default_archive_threshold"`
	Server                  ServerConfig   `toml:"server"`
	Actions                 []ActionConfig `toml:"actions"`
	// DomainRoutes maps URL patterns to override actions (F1).
	// Evaluated before scoped-auth; first-match wins.
	DomainRoutes []DomainRoute `toml:"domain_routes"`
}

// ServerConfig holds runtime knobs for `linkari serve` that previously lived
// only as command-line flags or LINKARI_* environment variables. Resolution
// order at startup: CLI flag > environment variable > config file > built-in
// default. The env var fallback preserves backward compatibility — existing
// LINKARI_* exports keep working unchanged.
//
// All fields are optional; an empty value means "fall back to env/default".
type ServerConfig struct {
	Port           int    `toml:"port"`
	Token          string `toml:"token"`           // discouraged: prefer LINKARI_TOKEN env
	JiraToken      string `toml:"jira_token"`      // EPIC-057: scoped bearer for ginit_* actions; ${secretsmanager:name#field} or literal
	JiraAPIUsername string `toml:"atlassian_email"`     // ${secretsmanager:linkari/jira-webhook#ATLASSIAN_EMAIL} or literal
	JiraAPIPassword string `toml:"atlassian_api_token"` // ${secretsmanager:linkari/jira-webhook#ATLASSIAN_API_TOKEN} or literal
	JiraDomain      string `toml:"jira_domain"`       // ${secretsmanager:linkari/jira-webhook#JIRA_DOMAIN} or literal
	PagerDutyToken              string `toml:"pagerduty_token"`               // ${secretsmanager:linkari/jira-webhook#PAGERDUTY_API_TOKEN} or literal
	GitHubToken                 string `toml:"github_token"`                  // ${secretsmanager:linkari/github-pat} or literal PAT
	GoogleServiceAccountPath    string `toml:"google_service_account_path"`   // path to service account JSON; ${secretsmanager:...} writes to cache dir
	AtlassianConfluenceToken    string `toml:"atlassian_confluence_token"`    // ${secretsmanager:linkari/confluence-token} or literal
	GoogleOAuthToken            string `toml:"google_oauth_token"`            // ${secretsmanager:linkari/google-oauth-token} or serialized oauth2.Token JSON
	QueueDB        string `toml:"queue_db"`
	FirebaseSA     string `toml:"firebase_sa"`
	LogFile        string `toml:"log_file"`
	Shell          string `toml:"shell"`
	ShellArgs      string `toml:"shell_args"`
	NotifyMinScore int    `toml:"notify_min_score"`
	ServerURL      string `toml:"server_url"` // base URL fish callbacks should use
	TSNetAuthKey   string `toml:"tsnet_authkey"` // EPIC-047: ${secretsmanager:...} or literal

	// EPIC-048: new fields for zero-flag boot.
	// Tsnet uses *bool so nil encodes "absent" (→ default true) vs explicit false.
	// Debug and NotifyMinScore remain plain-typed (zero-value == unset is safe).
	Tsnet         *bool  `toml:"tsnet"`
	TsnetHostname string `toml:"tsnet_hostname"`
	TsnetStateDir string `toml:"tsnet_state_dir"`
	Debug         bool   `toml:"debug"`

	// EPIC-051 M4: push gating config (per-profile throttle + default).
	Push PushYAMLConfig `toml:"push"`

	// EPIC-054 M3: relayed-state watchdog knobs. When both interval and
	// max age are zero the watchdog is disabled. Defaults applied by
	// RelayedWatchdogConfig() when unset: 60s interval, 900s max age.
	RelayedWatchdogInterval Duration `toml:"relayed_watchdog_interval"`
	RelayedWatchdogMaxAge   Duration `toml:"relayed_watchdog_max_age"`

	// EPIC-055 M1/M3: on-disk rescue + volume alert knobs.
	// UrlWorkDir defaults to $LINKARI_URL_WORK_DIR or $HOME/code/personal/url_work.
	RelayedWatchdogUrlWorkDir     string   `toml:"relayed_watchdog_url_work_dir"`
	RelayedWatchdogAlertThreshold int      `toml:"relayed_watchdog_alert_threshold"`
	RelayedWatchdogAlertWindow    Duration `toml:"relayed_watchdog_alert_window"`

	// Periodic VACUUM INTO snapshot. SnapshotInterval defaults to 1h; a
	// negative value disables the worker. SnapshotPath defaults to
	// <queue_db>.bak — a single rotating file so disk usage is bounded.
	SnapshotInterval Duration `toml:"snapshot_interval"`
	SnapshotPath     string   `toml:"snapshot_path"`

	// EPIC-052: share action resolution policy. Default is caller-wins —
	// the invariant check in resolveShareAction refuses to override a
	// non-empty received_action unless Share.HeuristicOverrideEnabled is true.
	Share ShareConfig `toml:"share"`

	// EPIC-067: voice note transcription config.
	WhisperModel string `toml:"whisper_model"` // path to ggml model file (default: ~/.local/share/whisper/ggml-large-v3-turbo.bin)
	FfmpegPath   string `toml:"ffmpeg_path"`   // path to ffmpeg binary (default: ffmpeg on PATH)

	// EPIC-009: YouTube transcription config.
	TranscriptsDir string       `toml:"transcripts_dir"` // directory for transcript markdown files (default: ~/code/personal/docs/transcripts)
	YtdlpPath      string       `toml:"ytdlp_path"`      // path to yt-dlp binary (default: yt-dlp on PATH)

	// EPIC-007: PDF document content extraction via LiteParse.
	LiteParseePath string        `toml:"liteparse_path"` // path to lit binary (default: lit on PATH; install: brew install llamaindex-liteparse)
	YouTube        YouTubeConfig `toml:"youtube"`        // EPIC-090 M5: per-field YouTube tuning

	// EPIC-001: Google Sign-In config.
	GoogleClientID     string `toml:"google_client_id"`     // ${secretsmanager:...} or literal; resolved via expandConfigRefs
	GoogleClientSecret string `toml:"google_client_secret"` // ${secretsmanager:...} or literal; required for YouTube API token refresh
	SessionTTLDays int      `toml:"session_ttl_days"` // session token TTL in days (default 90)
	InviteCodes    []string `toml:"invite_codes"`     // static invite codes seeded into DB at startup

	// EPIC-073: shield middleware config.
	Shield ShieldYAMLConfig `toml:"shield"`

	// EPIC-072 M6: cluster detection config.
	ClusterThreshold float64 `toml:"cluster_threshold"` // Jaccard threshold (default 0.4)
	ClusterMinItems  int     `toml:"cluster_min_items"`  // minimum items to form cluster (default 3)

	// EPIC-072 M9/M11: action routing config.
	ActionRouteThreshold int    `toml:"action_route_threshold"` // score threshold for action routes (default 80)
	ResearchDigestPath   string `toml:"research_digest_path"`   // path for research digest append (M11)

	// EPIC-080 M6: claude CLI path and vision model overrides.
	ClaudePath  string `toml:"claude_path"`  // path to claude binary (default: "claude" on PATH)
	VisionModel string `toml:"vision_model"` // model for vision scoring (default: claudeModel)

	// EPIC-081 M3: image noise gate — minimum file size in bytes to invoke
	// vision subprocess. Images below this threshold with no text metadata
	// are scored 0 without a vision API call. Default: 1024 (1KB).
	ImageNoiseGateMinBytes int64 `toml:"image_noise_gate_min_bytes"`

	// EPIC-083 M1-3: upper-bound file size gate — images above this threshold
	// skip vision scoring entirely. Default: 15MB (15 * 1024 * 1024).
	ImageNoiseGateMaxBytes int64 `toml:"image_noise_gate_max_bytes"`

	// EPIC-083 M2-3: per-call scoring cost ceiling (USD). When a single
	// eval.Evaluate call exceeds this amount, a score_cost_exceeded event
	// is logged. Monitoring only — does not block processing. Default: 0.05.
	MaxScoringCostUSD float64 `toml:"max_scoring_cost_usd"`

	// EPIC-084 M2: when true, prefilter skips (unsupported pipeline, login
	// wall, empty content, etc.) enqueue an FCM push so the user knows
	// their share was not scored. Default false to avoid spam during dev.
	NotifyOnPrefilterSkip bool `toml:"notify_on_prefilter_skip"`

	// EPIC-038 M1: gVisor sandbox config. When Sandbox.Enabled is true, all
	// ffmpeg/whisper/claude subprocess calls are routed through ContainerRuntime.
	Sandbox SandboxConfig `toml:"sandbox"`

	// EPIC-001 M3: IP blocklist — IPs and CIDRs rejected with 403 before routing.
	Blocklist []string `toml:"blocklist"`

	// EPIC-001 M3: CORS origins allowlist for FunnelMux. When empty,
	// falls back to "*" (wildcard). When set, only listed origins are allowed.
	CORSOrigins []string `toml:"cors_origins"`

	// EPIC-088 M4: override the built-in unsupported pipeline domain list.
	// When non-empty, replaces the compiled-in regex with a case-insensitive
	// OR match of these domain substrings. Default (empty): use built-in list.
	UnsupportedPipelineDomains []string `toml:"unsupported_pipeline_domains"`

	// GAP-07/GAP-08: metrics collection config. Controls whether MetricsCollector
	// is initialized at startup. Default (absent/nil): enabled. SIGHUP-reloadable.
	Metrics MetricsYAMLConfig `toml:"metrics"`
}

// ShareConfig controls how share requests map their received action/profile to
// a resolved action/profile (EPIC-052). The default (all zero values) enforces
// a strict caller-wins invariant: whatever the Android client sent in the
// share request's `action` field is preserved verbatim into the queue row. Any
// server-side heuristic that wants to override the caller MUST go through the
// feature flag below — and will still emit a `share_action_resolved` event
// with the override reason recorded.
type ShareConfig struct {
	// HeuristicOverrideEnabled, when true, allows resolveShareAction to
	// pick a different (action, profile) than the caller supplied (e.g. a
	// content heuristic that routes GitHub URLs to eng regardless of which
	// icon the user tapped). When false (the default), received_action
	// wins unconditionally.
	HeuristicOverrideEnabled bool `toml:"heuristic_override_enabled"`
}

// ShieldYAMLConfig is the on-disk shape of the `[server.shield]` block in config.toml.
// Controls the X-Linkari-Client header validation middleware on the Funnel mux.
type ShieldYAMLConfig struct {
	// Mode: "log" (default) emits debug logs for invalid/missing headers;
	// "enforce" returns 403.
	Mode string `toml:"mode"`
}

// MetricsYAMLConfig is the on-disk shape of the `[server.metrics]` block in config.toml.
// Controls MetricsCollector initialization for linkari.llm.cost_usd and related
// metric streams. Default (absent block): enabled.
type MetricsYAMLConfig struct {
	// Enabled, when explicitly set to false, prevents MetricsCollector
	// initialization at startup. When nil (absent from TOML) or true, metrics
	// collection is active. SIGHUP-reloadable via Server.reloadConfig.
	Enabled *bool `toml:"enabled"`
}

// MetricsEnabled returns true when metrics collection is active. The default
// when the `metrics:` block is absent is true — callers only skip initialization
// when Enabled is explicitly set to false.
func (s *ServerConfig) MetricsEnabled() bool {
	if s.Metrics.Enabled == nil {
		return true
	}
	return *s.Metrics.Enabled
}

// ShieldConfig returns the resolved shield mode. Empty mode defaults to "log".
func (s *ServerConfig) ShieldConfig() string {
	if s.Shield.Mode == "" {
		return "log"
	}
	return s.Shield.Mode
}

// PushYAMLConfig is the on-disk shape of the `[server.push]` block in config.toml.
// It is intentionally separate from the runtime PushConfig so the runtime
// struct can use strongly-typed time.Duration while TOML stays human-friendly
// (duration strings like "1h", "24h").
type PushYAMLConfig struct {
	// DigestThrottle maps a profile name → throttle window duration string.
	// Example: {eng = "1h", dining = "24h"}.
	DigestThrottle DurationMap `toml:"digest_throttle"`
	// DigestThrottleDefault is the fallback window for profiles not listed
	// in DigestThrottle. Default "1h" when empty.
	DigestThrottleDefault Duration `toml:"digest_throttle_default"`
}

// Duration is a TOML-friendly wrapper around time.Duration that parses
// strings via time.ParseDuration.
type Duration struct{ D time.Duration }

// UnmarshalText implements encoding.TextUnmarshaler for duration strings.
// Works for both TOML and JSON decoders.
func (d *Duration) UnmarshalText(text []byte) error {
	s := string(text)
	if s == "" {
		d.D = 0
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", s, err)
	}
	d.D = parsed
	return nil
}

// Duration returns the underlying time.Duration. Zero value is 0.
func (d Duration) Duration() time.Duration { return d.D }

// DurationMap is a YAML-friendly map of string → Duration.
type DurationMap map[string]Duration

// Durations returns a plain map[string]time.Duration suitable for runtime.
func (m DurationMap) Durations() map[string]time.Duration {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]time.Duration, len(m))
	for k, v := range m {
		out[k] = v.D
	}
	return out
}


// TemplateData is the data passed to command templates.
type TemplateData struct {
	URL     string
	Text    string
	Profile string
	Match   string // regex match result (kind=regex)
	Slug    string
}

// ShellQuoted returns a copy with user-supplied fields (URL, Text, Match)
// wrapped in shell-safe single quotes. Profile and Slug are server-derived
// and not quoted.
func (d TemplateData) ShellQuoted() TemplateData {
	d.URL = shellQuote(d.URL)
	d.Text = shellQuote(d.Text)
	d.Match = shellQuote(d.Match)
	return d
}

// defaultConfigPath returns ~/.config/linkari/config.toml.
func defaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "linkari", "config.toml")
}

// expandConfigRefs resolves ${env:VAR}, ${file:/path}, ${secretsmanager:name#field},
// and bare ${VAR} references in the raw config string before TOML parsing.
func expandConfigRefs(ctx context.Context, s string) string {
	cache := make(map[string]string)
	var resolver *secrets.Resolver

	return os.Expand(s, func(key string) string {
		source, rest, hasScheme := strings.Cut(key, ":")
		if !hasScheme {
			return os.Getenv(key)
		}
		switch source {
		case "env":
			return os.Getenv(rest)
		case "file":
			data, _ := os.ReadFile(rest)
			return strings.TrimSpace(string(data))
		case "secretsmanager":
			secretName, field, hasField := strings.Cut(rest, "#")
			if cached, ok := cache[secretName]; ok {
				return extractJSONField(cached, field, hasField)
			}
			if resolver == nil {
				resolver = secrets.New(secrets.DefaultAWSFactory())
			}
			raw, _, err := resolver.Resolve(ctx, "secretsmanager://"+secretName)
			if err != nil {
				return ""
			}
			cache[secretName] = raw
			return extractJSONField(raw, field, hasField)
		default:
			return ""
		}
	})
}

// extractJSONField returns the named field from a JSON object string, or the
// raw string itself when hasField is false.
func extractJSONField(raw, field string, hasField bool) string {
	if !hasField {
		return raw
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return raw
	}
	if v, ok := obj[field].(string); ok {
		return v
	}
	return ""
}

// LoadConfig reads, expands refs, and validates the TOML config file.
func LoadConfig(ctx context.Context, path string) (*Config, error) {
	if path == "" {
		path = defaultConfigPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	expanded := expandConfigRefs(ctx, string(data))
	var cfg Config
	if _, err := toml.Decode(expanded, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	// EPIC-051 M5: merge the on-disk file on top of the builtin action list
	// by ID so operators can override individual fields without having to
	// re-declare every builtin action. A user file with zero actions cleanly
	// inherits all builtins — previously it would wipe the list entirely.
	merged, err := MergeWithBuiltin(builtinConfig(), &cfg)
	if err != nil {
		return nil, fmt.Errorf("merge config: %w", err)
	}
	return merged, nil
}

// validate checks all actions for correctness and compiles templates/regexes.
func (c *Config) validate() error {
	ids := make(map[string]bool, len(c.Actions))
	for i := range c.Actions {
		a := &c.Actions[i]
		if a.ID == "" {
			return fmt.Errorf("action %d: id is required", i)
		}
		if ids[a.ID] {
			return fmt.Errorf("duplicate action id %q", a.ID)
		}
		ids[a.ID] = true

		if a.Kind == "" {
			a.Kind = KindTemplate // default
		}

		switch a.Kind {
		case KindTemplate:
			if a.CommandTemplate == "" {
				return fmt.Errorf("action %q: command_template required for kind=template", a.ID)
			}
			t, err := template.New(a.ID).Parse(a.CommandTemplate)
			if err != nil {
				return fmt.Errorf("action %q: invalid template: %w", a.ID, err)
			}
			a.compiledTemplate = t

		case KindLiteral:
			// No template needed — text is sent literally.

		case KindRegex:
			if a.Pattern == "" {
				return fmt.Errorf("action %q: pattern required for kind=regex", a.ID)
			}
			if a.CommandTemplate == "" {
				return fmt.Errorf("action %q: command_template required for kind=regex", a.ID)
			}
			re, err := regexp.Compile(a.Pattern)
			if err != nil {
				return fmt.Errorf("action %q: invalid pattern: %w", a.ID, err)
			}
			a.compiledRegex = re
			t, err := template.New(a.ID).Parse(a.CommandTemplate)
			if err != nil {
				return fmt.Errorf("action %q: invalid template: %w", a.ID, err)
			}
			a.compiledTemplate = t

		case KindCapture:
			// F2 stub: no template required. captureAsync is not yet implemented.

		default:
			return fmt.Errorf("action %q: unknown kind %q", a.ID, a.Kind)
		}

		// EPIC-058 M3: auto_launch requires a positive confidence_threshold.
		if a.AutoLaunch && a.ConfidenceThreshold <= 0 {
			return fmt.Errorf("action %q: auto_launch requires confidence_threshold > 0", a.ID)
		}
	}
	return nil
}

// ActiveActions returns actions whose conditions are met.
func (c *Config) ActiveActions() []ActionConfig {
	var active []ActionConfig
	for _, a := range c.Actions {
		if a.Condition != "" && !evalCondition(a.Condition) {
			continue
		}
		active = append(active, a)
	}
	return active
}

// evalCondition evaluates a simple condition string.
// Supported: "env:VAR=VALUE" — checks os.Getenv(VAR) == VALUE.
func evalCondition(cond string) bool {
	if strings.HasPrefix(cond, "env:") {
		rest := strings.TrimPrefix(cond, "env:")
		parts := strings.SplitN(rest, "=", 2)
		if len(parts) != 2 {
			return false
		}
		return os.Getenv(parts[0]) == parts[1]
	}
	return false
}

// RenderCommand renders the command string for a template/regex action.
// User-supplied fields (URL, Text, Match) are automatically shell-quoted
// to prevent injection via URL payloads (GAP-9).
func (a *ActionConfig) RenderCommand(data TemplateData) (string, error) {
	if a.compiledTemplate == nil {
		return "", fmt.Errorf("action %q: no compiled template", a.ID)
	}
	data = data.ShellQuoted()
	var buf strings.Builder
	if err := a.compiledTemplate.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("action %q: template exec: %w", a.ID, err)
	}
	return buf.String(), nil
}

// ExtractMatch runs the compiled regex against text and returns the first match.
func (a *ActionConfig) ExtractMatch(text string) string {
	if a.compiledRegex == nil {
		return ""
	}
	return a.compiledRegex.FindString(text)
}

// ToAction converts an ActionConfig to the API-facing Action struct.
func (a *ActionConfig) ToAction() Action {
	return Action{
		ID:     a.ID,
		Label:  a.Label,
		Icon:   a.Icon,
		Type:   a.Type,
		Target: a.Target,
	}
}

// MergeWithBuiltin returns a new Config where `user` is overlaid on top of
// `builtin` using merge-by-ID semantics for the action list:
//
//   - Every builtin action is present in the result.
//   - If the user file defines an action with the same ID, its non-zero
//     fields override the builtin's fields (shallow merge — compiled
//     template sources are not deep-merged).
//   - Extra actions in the user file (IDs not present in builtin) are
//     appended verbatim.
//   - Top-level scalars (DefaultArchiveThreshold, Server) are taken from
//     the user file when non-zero; otherwise the builtin's value wins.
//
// The returned Config is re-validated so compiled templates/regexes on the
// merged actions are populated. Callers must handle the error.
//
// EPIC-051 M5: replaces the wholesale "user file replaces builtin" behavior
// of LoadConfig with a forgiving merge. See EPIC-050 PoMo action #7.
func MergeWithBuiltin(builtin, user *Config) (*Config, error) {
	if user == nil {
		// Nothing to merge: return a validated copy of the builtin.
		clone := *builtin
		clone.Actions = append([]ActionConfig(nil), builtin.Actions...)
		if err := clone.validate(); err != nil {
			return nil, err
		}
		return &clone, nil
	}

	out := Config{
		DefaultArchiveThreshold: builtin.DefaultArchiveThreshold,
		Server:                  builtin.Server,
		DomainRoutes:            builtin.DomainRoutes,
	}
	if user.DefaultArchiveThreshold != 0 {
		out.DefaultArchiveThreshold = user.DefaultArchiveThreshold
	}
	if !user.Server.IsZero() {
		out.Server = user.Server
	}
	if len(user.DomainRoutes) > 0 {
		out.DomainRoutes = user.DomainRoutes
	}

	// Index user actions by ID for O(1) override lookups.
	userByID := make(map[string]*ActionConfig, len(user.Actions))
	for i := range user.Actions {
		userByID[user.Actions[i].ID] = &user.Actions[i]
	}

	// Walk builtins, overlay matching user fields.
	seen := make(map[string]bool, len(builtin.Actions))
	for _, b := range builtin.Actions {
		merged := b
		if u, ok := userByID[b.ID]; ok {
			merged = mergeActionShallow(b, *u)
			seen[b.ID] = true
		}
		out.Actions = append(out.Actions, merged)
	}
	// Append any user-only actions.
	for _, u := range user.Actions {
		if !seen[u.ID] {
			out.Actions = append(out.Actions, u)
		}
	}

	if err := out.validate(); err != nil {
		return nil, fmt.Errorf("merged config invalid: %w", err)
	}
	return &out, nil
}

// mergeActionShallow overlays non-zero user fields on top of a builtin action.
// Integer -1 (explicit "no auto-archive") is treated as a real override.
func mergeActionShallow(base, user ActionConfig) ActionConfig {
	out := base
	// Preserve ID from base (they match by construction).
	if user.Label != "" {
		out.Label = user.Label
	}
	if user.Icon != "" {
		out.Icon = user.Icon
	}
	if user.Type != "" {
		out.Type = user.Type
	}
	if user.Target != "" {
		out.Target = user.Target
	}
	if user.Kind != "" {
		out.Kind = user.Kind
	}
	if user.CommandTemplate != "" {
		out.CommandTemplate = user.CommandTemplate
		// Clear compiled template so validate() recompiles.
		out.compiledTemplate = nil
	}
	if user.Pattern != "" {
		out.Pattern = user.Pattern
		out.compiledRegex = nil
	}
	// ArchiveThreshold: 0 = "inherit", any other value (including -1) wins.
	if user.ArchiveThreshold != 0 {
		out.ArchiveThreshold = user.ArchiveThreshold
	}
	if user.ProfileMap != "" {
		out.ProfileMap = user.ProfileMap
	}
	if user.Condition != "" {
		out.Condition = user.Condition
	}
	if user.InlineTriage {
		out.InlineTriage = true
	}
	if user.AutoScore {
		out.AutoScore = true
	}
	if user.ConfidenceThreshold != 0 {
		out.ConfidenceThreshold = user.ConfidenceThreshold
	}
	if user.AutoLaunch {
		out.AutoLaunch = true
	}
	if user.ServerScore {
		out.ServerScore = true
	}
	return out
}

// builtinConfig returns the hardcoded default config with two auto-profile
// actions: uinit_auto (score any URL server-side) and ginit_auto (build a
// Jira workspace). Profile selection is deferred to server-side heuristics
// in resolveShareAction (EPIC-061).
func builtinConfig() *Config {
	cfg := &Config{
		DefaultArchiveThreshold: 80,
		Actions: []ActionConfig{
			{
				ID:              "uinit_auto",
				Label:           "Score",
				Icon:            "auto",
				Type:            "url",
				Target:          "linkari:0",
				Kind:            KindTemplate,
				CommandTemplate: `uinit --auto-resume --profile {{.Profile}} {{.URL}}`,
				ProfileMap:      "auto",
				ServerScore:     true,
			},
			{
				ID:              "vnote_auto",
				Label:           "Transcribe",
				Icon:            "mic",
				Type:            "audio,url", // EPIC-090 M1: added "url" so YouTube shares reach vnote_auto
				Target:          "linkari:0",
				Kind:            KindTemplate,
				CommandTemplate: "echo vnote", // stub — never rendered when ServerScore=true
				ProfileMap:      "auto",
				ServerScore:     true,
			},
			{
				ID:              "ginit_auto",
				Label:           "Build",
				Icon:            "work",
				Type:            "text",
				Target:          "linkari:0",
				Kind:            KindTemplate,
				CommandTemplate: "ginit {{.Text}}",
				ProfileMap:      "auto",
				AutoScore:       true,
			},
			// F2: capture actions — wired with ArtifactDir/ArtifactFilenameTemplate.
			// JiraRenderer registered at startup (F3); confluence renderer deferred to F6.
			{
				ID:                      "capture_jira_auto",
				Label:                   "Capture Jira",
				Icon:                    "work",
				Type:                    "url",
				Kind:                    KindCapture,
				ProfileMap:              "auto",
				ArtifactDir:             "docs/captures",
				ArtifactFilenameTemplate: "{{.Date}}_{{.Key}}.md",
			},
			{
				ID:                      "capture_confluence_auto",
				Label:                   "Capture Confluence",
				Icon:                    "work",
				Type:                    "url",
				Kind:                    KindCapture,
				ProfileMap:              "auto",
				ArtifactDir:             "docs/captures",
				ArtifactFilenameTemplate: "{{.Date}}_{{.Key}}.md",
			},
		},
	}
	// Compile templates/regexes.
	cfg.validate()
	return cfg
}
