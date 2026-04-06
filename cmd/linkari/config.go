package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

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
	ProfileMap       string     `yaml:"profile_map"`       // "prefix" = extract profile from id prefix (e.g. uinit_eng → eng)
	Condition        string     `yaml:"condition,omitempty"` // "env:VAR=VALUE" — only register when condition met
	InlineTriage     bool       `yaml:"inline_triage,omitempty"` // EPIC-043 M5: run command headlessly, skip tmux window (fire-and-forget)

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
	QueueDB        string `yaml:"queue_db"`
	FirebaseSA     string `yaml:"firebase_sa"`
	LogFile        string `yaml:"log_file"`
	Shell          string `yaml:"shell"`
	ShellArgs      string `yaml:"shell_args"`
	NotifyMinScore int    `yaml:"notify_min_score"`
	ServerURL      string `yaml:"server_url"` // base URL fish callbacks should use
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

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &cfg, nil
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

// builtinConfig returns the hardcoded default config matching the original Go structs.
// Used as fallback when no actions.yaml exists.
func builtinConfig() *Config {
	cfg := &Config{
		DefaultArchiveThreshold: 80,
		Actions: []ActionConfig{
			{ID: "uinit_eng", Label: "Linkari (Eng)", Icon: "eng", Type: "url", Target: "linkari:0", Kind: KindTemplate,
				CommandTemplate: `uinit --auto-resume {{if and .Profile (ne .Profile "eng")}}--profile {{.Profile}} {{end}}{{.URL}}`,
				ProfileMap: "prefix", ArchiveThreshold: 80},
			{ID: "uinit_life", Label: "Linkari (Life)", Icon: "life", Type: "url", Target: "linkari:0", Kind: KindTemplate,
				CommandTemplate: `uinit --auto-resume --profile {{.Profile}} {{.URL}}`,
				ProfileMap: "prefix", ArchiveThreshold: -1},
			{ID: "uinit_travel", Label: "Linkari (Travel)", Icon: "travel", Type: "url", Target: "linkari:0", Kind: KindTemplate,
				CommandTemplate: `uinit --auto-resume --profile {{.Profile}} {{.URL}}`,
				ProfileMap: "prefix", ArchiveThreshold: 80},
			{ID: "uinit_fashion", Label: "Linkari (Fashion)", Icon: "fashion", Type: "url", Target: "linkari:0", Kind: KindTemplate,
				CommandTemplate: `uinit --auto-resume --profile {{.Profile}} {{.URL}}`,
				ProfileMap: "prefix", ArchiveThreshold: 80},
			{ID: "uinit_music", Label: "Linkari (Music)", Icon: "music", Type: "url", Target: "linkari:0", Kind: KindTemplate,
				CommandTemplate: `uinit --auto-resume --profile {{.Profile}} {{.URL}}`,
				ProfileMap: "prefix", ArchiveThreshold: 80},
			{ID: "uinit_finance", Label: "Linkari (Finance)", Icon: "finance", Type: "url", Target: "linkari:0", Kind: KindTemplate,
				CommandTemplate: `uinit --auto-resume --profile {{.Profile}} {{.URL}}`,
				ProfileMap: "prefix", ArchiveThreshold: 70},
			{ID: "uinit_dining", Label: "Linkari (Dining)", Icon: "dining", Type: "url", Target: "linkari:0", Kind: KindTemplate,
				CommandTemplate: `uinit --auto-resume --profile {{.Profile}} {{.URL}}`,
				ProfileMap: "prefix", ArchiveThreshold: 70},
			{ID: "ginit", Label: "ginit", Icon: "work", Type: "text", Target: "JIRA:0", Kind: KindRegex,
				Pattern: `[A-Z][A-Z0-9]+-[0-9]+`, CommandTemplate: "ginit {{.Match}} --yolo",
				Condition: "env:ATLASSIAN_DOMAIN=grindr.atlassian.net"},
		},
	}
	// Compile templates/regexes.
	cfg.validate()
	return cfg
}
