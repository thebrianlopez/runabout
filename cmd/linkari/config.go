package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"
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

// resolvePushConfigOnce loads ~/.config/linkari/server.yaml (if present) and
// installs its push settings on the given queue. Used by the CLI score path
// so `linkari score` honors the operator's configured throttles / min-score
// floor without booting a server. Errors are swallowed intentionally — the
// CLI should fall back to zero-value defaults rather than refuse to score.
func resolvePushConfigOnce(q *Queue) {
	if q == nil {
		return
	}
	cfg, err := LoadServerFile(defaultServerConfigPath())
	if err != nil || cfg == nil {
		q.SetPushConfig(&PushConfig{})
		return
	}
	q.SetPushConfig(cfg.PushConfig())
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
		!s.Share.HeuristicOverrideEnabled &&
		s.WhisperModel == "" && s.FfmpegPath == "" &&
		s.GoogleClientID == "" && s.SessionTTLDays == 0
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

// ActionKind determines how a share request is dispatched.
type ActionKind string

const (
	// KindTemplate renders a Go text/template with request fields.
	KindTemplate ActionKind = "template"
	// KindLiteral pastes text literally into a tmux target (no execution).
	KindLiteral ActionKind = "literal"
	// KindRegex extracts a match from text/URL before rendering a command template.
	KindRegex ActionKind = "regex"
)

// ActionConfig defines a single share action in the YAML config.
type ActionConfig struct {
	ID               string     `yaml:"id"`
	Label            string     `yaml:"label"`
	Icon             string     `yaml:"icon"`
	Type             string     `yaml:"type"`           // "url" or "text"
	Target           string     `yaml:"target"`         // tmux target "session:pane"
	Kind             ActionKind `yaml:"kind"`            // template, literal, regex
	CommandTemplate  string     `yaml:"command_template"` // Go text/template string
	Pattern          string     `yaml:"pattern"`          // regex for kind=regex
	ArchiveThreshold int        `yaml:"archive_threshold"` // -1 = no auto-archive
	ProfileMap       string     `yaml:"profile_map"`       // "prefix" = extract from id prefix; "auto" = server-side heuristic (EPIC-061)
	Condition        string     `yaml:"condition,omitempty"` // "env:VAR=VALUE" — only register when condition met
	InlineTriage        bool `yaml:"inline_triage,omitempty"`        // EPIC-043 M5: run command headlessly, skip tmux window (fire-and-forget)
	AutoScore           bool `yaml:"auto_score,omitempty"`           // EPIC-057: enqueue as scored immediately (skip watchdog)
	ConfidenceThreshold int  `yaml:"confidence_threshold,omitempty"` // EPIC-058 M3: minimum score to pass confidence gate (0 = no gate)
	AutoLaunch          bool `yaml:"auto_launch,omitempty"`          // EPIC-058 M3: auto-launch ginit when gate passes (requires confidence_threshold > 0)
	ServerScore         bool `yaml:"server_score,omitempty"`         // EPIC-060: score uinit_* actions server-side via Jina+Haiku (no tmux window)

	// Parsed fields (not in YAML)
	compiledTemplate *template.Template
	compiledRegex    *regexp.Regexp
}

// Config is the top-level YAML config file.
type Config struct {
	DefaultArchiveThreshold int            `yaml:"default_archive_threshold"`
	Server                  ServerConfig   `yaml:"server"`
	Actions                 []ActionConfig `yaml:"actions"`
}

// ServerConfig holds runtime knobs for `linkari serve` that previously lived
// only as command-line flags or LINKARI_* environment variables. Resolution
// order at startup: CLI flag > environment variable > config file > built-in
// default. The env var fallback preserves backward compatibility — existing
// LINKARI_* exports keep working unchanged.
//
// All fields are optional; an empty value means "fall back to env/default".
type ServerConfig struct {
	Port           int    `yaml:"port"`
	Token          string `yaml:"token"`           // discouraged: prefer LINKARI_TOKEN env
	JiraToken      string `yaml:"jira_token"`      // EPIC-057: scoped bearer for ginit_* actions; secretsmanager:// URI or literal
	JiraAPIUsername string `yaml:"jira_api_username"` // secretsmanager://linkari/jira-webhook#JIRA_API_USERNAME or literal
	JiraAPIPassword string `yaml:"jira_api_password"` // secretsmanager://linkari/jira-webhook#JIRA_API_PASSWORD or literal
	JiraDomain      string `yaml:"jira_domain"`       // secretsmanager://linkari/jira-webhook#JIRA_DOMAIN or literal
	PagerDutyToken  string `yaml:"pagerduty_token"`   // secretsmanager://linkari/jira-webhook#PAGERDUTY_API_TOKEN or literal
	QueueDB        string `yaml:"queue_db"`
	FirebaseSA     string `yaml:"firebase_sa"`
	LogFile        string `yaml:"log_file"`
	Shell          string `yaml:"shell"`
	ShellArgs      string `yaml:"shell_args"`
	NotifyMinScore int    `yaml:"notify_min_score"`
	ServerURL      string `yaml:"server_url"` // base URL fish callbacks should use
	TSNetAuthKey   string `yaml:"tsnet_authkey"` // EPIC-047: secretsmanager:// URI or literal

	// EPIC-048: new fields for zero-flag boot.
	// Tsnet uses *bool so nil encodes "absent" (→ default true) vs explicit false.
	// Debug and NotifyMinScore remain plain-typed (zero-value == unset is safe).
	Tsnet         *bool  `yaml:"tsnet"`
	TsnetHostname string `yaml:"tsnet_hostname"`
	TsnetStateDir string `yaml:"tsnet_state_dir"`
	Debug         bool   `yaml:"debug"`

	// EPIC-051 M4: push gating config (per-profile throttle + default).
	Push PushYAMLConfig `yaml:"push"`

	// EPIC-054 M3: relayed-state watchdog knobs. When both interval and
	// max age are zero the watchdog is disabled. Defaults applied by
	// RelayedWatchdogConfig() when unset: 60s interval, 900s max age.
	RelayedWatchdogInterval Duration `yaml:"relayed_watchdog_interval"`
	RelayedWatchdogMaxAge   Duration `yaml:"relayed_watchdog_max_age"`

	// EPIC-055 M1/M3: on-disk rescue + volume alert knobs.
	// UrlWorkDir defaults to $LINKARI_URL_WORK_DIR or $HOME/code/personal/url_work.
	RelayedWatchdogUrlWorkDir     string   `yaml:"relayed_watchdog_url_work_dir"`
	RelayedWatchdogAlertThreshold int      `yaml:"relayed_watchdog_alert_threshold"`
	RelayedWatchdogAlertWindow    Duration `yaml:"relayed_watchdog_alert_window"`

	// EPIC-052: share action resolution policy. Default is caller-wins —
	// the invariant check in resolveShareAction refuses to override a
	// non-empty received_action unless Share.HeuristicOverrideEnabled is true.
	Share ShareConfig `yaml:"share"`

	// EPIC-067: voice note transcription config.
	WhisperModel string `yaml:"whisper_model,omitempty"` // path to ggml model file (default: ~/.local/share/whisper/ggml-large-v3-turbo.bin)
	FfmpegPath   string `yaml:"ffmpeg_path,omitempty"`   // path to ffmpeg binary (default: ffmpeg on PATH)

	// EPIC-001: Google Sign-In config.
	GoogleClientID string `yaml:"google_client_id"` // secretsmanager:// URI or literal; resolved via resolveField pipeline
	SessionTTLDays int    `yaml:"session_ttl_days"` // session token TTL in days (default 90)
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
	HeuristicOverrideEnabled bool `yaml:"heuristic_override_enabled"`
}

// PushYAMLConfig is the on-disk shape of the `push:` block in server.yaml.
// It is intentionally separate from the runtime PushConfig so the runtime
// struct can use strongly-typed time.Duration while YAML stays human-friendly
// (duration strings like "1h", "24h").
type PushYAMLConfig struct {
	// DigestThrottle maps a profile name → throttle window duration string.
	// Example: {eng: "1h", dining: "24h"}.
	DigestThrottle DurationMap `yaml:"digest_throttle"`
	// DigestThrottleDefault is the fallback window for profiles not listed
	// in DigestThrottle. Default "1h" when empty.
	DigestThrottleDefault Duration `yaml:"digest_throttle_default"`
}

// Duration is a YAML-friendly wrapper around time.Duration that parses
// strings via time.ParseDuration.
type Duration struct{ D time.Duration }

// UnmarshalYAML implements yaml.Unmarshaler for duration strings.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Value == "" {
		d.D = 0
		return nil
	}
	parsed, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", value.Value, err)
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

// ServerFile is the on-disk shape of ~/.config/linkari/server.yaml. It wraps
// ServerConfig under a top-level `server:` key so the file's layout matches
// the deprecated [server:] block in actions.yaml. Introduced by EPIC-047 M3.
type ServerFile struct {
	Server ServerConfig `yaml:"server"`
}

// LoadServerFile reads ~/.config/linkari/server.yaml (or another path) and
// returns the parsed ServerConfig. Returns (nil, nil) if the file does not
// exist — callers fall through to the deprecated actions.yaml[server:] block.
func LoadServerFile(path string) (*ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read server.yaml: %w", err)
	}
	var sf ServerFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("parse server.yaml: %w", err)
	}
	return &sf.Server, nil
}

// defaultServerConfigPath returns ~/.config/linkari/server.yaml.
func defaultServerConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "linkari", "server.yaml")
}

// TemplateData is the data passed to command templates.
type TemplateData struct {
	URL     string
	Text    string
	Profile string
	Match   string // regex match result (kind=regex)
	Slug    string
}

// defaultConfigPath returns ~/.config/linkari/actions.yaml.
func defaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "linkari", "actions.yaml")
}

// LoadConfig reads and validates the action config file.
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		path = defaultConfigPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
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
func (a *ActionConfig) RenderCommand(data TemplateData) (string, error) {
	if a.compiledTemplate == nil {
		return "", fmt.Errorf("action %q: no compiled template", a.ID)
	}
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
	}
	if user.DefaultArchiveThreshold != 0 {
		out.DefaultArchiveThreshold = user.DefaultArchiveThreshold
	}
	if !user.Server.IsZero() {
		out.Server = user.Server
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
				Type:            "audio",
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
		},
	}
	// Compile templates/regexes.
	cfg.validate()
	return cfg
}
