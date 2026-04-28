// Package config loads and validates the jira-poller runtime configuration.
// Env vars are the primary source; an optional YAML file (CONFIG_FILE) provides
// defaults that env vars always override.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Sentinel errors for configuration failures.
var (
	// ErrConfigMissing is returned when a required field is absent after all
	// sources have been merged. The error message names the missing field.
	ErrConfigMissing = errors.New("config: required field missing")

	// ErrConfigInvalid is returned when a field value fails a semantic check
	// (e.g. LookbackWindow <= PollInterval).
	ErrConfigInvalid = errors.New("config: invalid value")
)

// Config holds the fully-resolved runtime configuration for the jira-poller.
// All durations default to safe values; see Load() for details.
type Config struct {
	// Jira connection.
	JiraBaseURL  string   // required; JIRA_BASE_URL
	JiraProjects []string // required; JIRA_PROJECTS (comma-separated)

	// Polling behaviour.
	PollInterval   time.Duration // default 60s; POLL_INTERVAL
	LookbackWindow time.Duration // default 120s; LOOKBACK_WINDOW; must be > PollInterval

	// Storage (F5 SQLite backends).
	DBPath  string // required; DB_PATH — path to SQLite file
	SinkDir string // optional; SINK_DIR — JSONL output dir (defaults to ~/.automation-metrics/events)

	// Credentials.
	SecretARN     string        // required unless LocalDev=true; SECRET_ARN
	LocalDev      bool          // LOCAL_DEV=true → file-based credentials
	CredentialTTL time.Duration // default 6h; CREDENTIAL_TTL

	// Observability.
	LogFormat    string // "json"|"text"; default "json"; LOG_FORMAT
	OTelEndpoint string // optional; OTEL_EXPORTER_OTLP_ENDPOINT

	// Optional YAML overlay path.
	ConfigFile string // CONFIG_FILE
}

// yamlConfig mirrors Config for YAML unmarshalling with snake_case keys.
type yamlConfig struct {
	JiraBaseURL    string   `yaml:"jira_base_url"`
	JiraProjects   []string `yaml:"jira_projects"`
	PollInterval   string   `yaml:"poll_interval"`
	LookbackWindow string   `yaml:"lookback_window"`
	DBPath         string   `yaml:"db_path"`
	SinkDir        string   `yaml:"sink_dir"`
	SecretARN      string   `yaml:"secret_arn"`
	LocalDev       *bool    `yaml:"local_dev"`
	CredentialTTL  string   `yaml:"credential_ttl"`
	LogFormat      string   `yaml:"log_format"`
	OTelEndpoint   string   `yaml:"otel_endpoint"`
}

// Load reads env vars, merges optional YAML at CONFIG_FILE, and validates.
// Env vars always win over YAML values. Returns ErrConfigMissing or
// ErrConfigInvalid on failure.
func Load() (Config, error) {
	cfg := defaults()

	// Optional YAML overlay — applied before env so env wins.
	if path := os.Getenv("CONFIG_FILE"); path != "" {
		cfg.ConfigFile = path
		if err := mergeYAML(&cfg, path); err != nil {
			return Config{}, fmt.Errorf("%w: yaml overlay: %s", ErrConfigInvalid, err)
		}
	}

	// Env overrides YAML (always wins).
	applyEnv(&cfg)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks semantic invariants that Load cannot enforce from env alone.
func (c Config) Validate() error {
	if c.JiraBaseURL == "" {
		return fmt.Errorf("%w: JIRA_BASE_URL", ErrConfigMissing)
	}
	if len(c.JiraProjects) == 0 {
		return fmt.Errorf("%w: JIRA_PROJECTS", ErrConfigMissing)
	}
	if !c.LocalDev && c.SecretARN == "" {
		return fmt.Errorf("%w: SECRET_ARN (or set LOCAL_DEV=true for file-based credentials)", ErrConfigMissing)
	}
	if c.LookbackWindow <= c.PollInterval {
		return fmt.Errorf("%w: LOOKBACK_WINDOW (%s) must be greater than POLL_INTERVAL (%s)",
			ErrConfigInvalid, c.LookbackWindow, c.PollInterval)
	}
	return nil
}

func defaults() Config {
	return Config{
		PollInterval:   60 * time.Second,
		LookbackWindow: 120 * time.Second,
		LogFormat:      "json",
		CredentialTTL:  6 * time.Hour,
	}
}

func mergeYAML(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var yc yamlConfig
	if err := yaml.Unmarshal(data, &yc); err != nil {
		return err
	}
	if yc.JiraBaseURL != "" {
		cfg.JiraBaseURL = yc.JiraBaseURL
	}
	if len(yc.JiraProjects) > 0 {
		cfg.JiraProjects = yc.JiraProjects
	}
	if yc.PollInterval != "" {
		d, err := time.ParseDuration(yc.PollInterval)
		if err != nil {
			return fmt.Errorf("poll_interval: %w", err)
		}
		cfg.PollInterval = d
	}
	if yc.LookbackWindow != "" {
		d, err := time.ParseDuration(yc.LookbackWindow)
		if err != nil {
			return fmt.Errorf("lookback_window: %w", err)
		}
		cfg.LookbackWindow = d
	}
	if yc.DBPath != "" {
		cfg.DBPath = yc.DBPath
	}
	if yc.SinkDir != "" {
		cfg.SinkDir = yc.SinkDir
	}
	if yc.SecretARN != "" {
		cfg.SecretARN = yc.SecretARN
	}
	if yc.LocalDev != nil {
		cfg.LocalDev = *yc.LocalDev
	}
	if yc.CredentialTTL != "" {
		d, err := time.ParseDuration(yc.CredentialTTL)
		if err != nil {
			return fmt.Errorf("credential_ttl: %w", err)
		}
		cfg.CredentialTTL = d
	}
	if yc.LogFormat != "" {
		cfg.LogFormat = yc.LogFormat
	}
	if yc.OTelEndpoint != "" {
		cfg.OTelEndpoint = yc.OTelEndpoint
	}
	return nil
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("JIRA_BASE_URL"); v != "" {
		cfg.JiraBaseURL = v
	}
	if v := os.Getenv("JIRA_PROJECTS"); v != "" {
		cfg.JiraProjects = splitComma(v)
	}
	if v := os.Getenv("POLL_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.PollInterval = d
		}
	}
	if v := os.Getenv("LOOKBACK_WINDOW"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.LookbackWindow = d
		}
	}
	if v := os.Getenv("DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("SINK_DIR"); v != "" {
		cfg.SinkDir = v
	}
	if v := os.Getenv("SECRET_ARN"); v != "" {
		cfg.SecretARN = v
	}
	if v := os.Getenv("LOCAL_DEV"); v != "" {
		cfg.LocalDev = v == "true"
	}
	if v := os.Getenv("CREDENTIAL_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.CredentialTTL = d
		}
	}
	if v := os.Getenv("LOG_FORMAT"); v != "" {
		cfg.LogFormat = v
	}
	if v := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" {
		cfg.OTelEndpoint = v
	}
	if v := os.Getenv("CONFIG_FILE"); v != "" {
		cfg.ConfigFile = v
	}
}

func splitComma(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
