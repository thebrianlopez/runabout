package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blo-grindr/runabout/cmd/jira-poller/internal/config"
)

// clearEnv unsets all env vars used by the config package to prevent test
// pollution from the caller's environment.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"JIRA_BASE_URL", "JIRA_PROJECTS", "POLL_INTERVAL", "LOOKBACK_WINDOW",
		"DB_PATH", "SINK_DIR", "SECRET_ARN", "LOCAL_DEV", "CREDENTIAL_TTL",
		"LOG_FORMAT", "OTEL_EXPORTER_OTLP_ENDPOINT", "CONFIG_FILE",
	} {
		t.Setenv(k, "")
	}
}

// setMinimalValid sets the minimum env vars for a valid config.
func setMinimalValid(t *testing.T) {
	t.Helper()
	t.Setenv("JIRA_BASE_URL", "https://jira.example.com")
	t.Setenv("JIRA_PROJECTS", "INFRA")
	t.Setenv("LOCAL_DEV", "true")
	t.Setenv("POLL_INTERVAL", "30s")
	t.Setenv("LOOKBACK_WINDOW", "90s")
}

// CT-1: Missing JIRA_BASE_URL → ErrConfigMissing naming the field.
func TestLoad_CT1_MissingRequired_JiraBaseURL(t *testing.T) {
	clearEnv(t)
	t.Setenv("JIRA_PROJECTS", "INFRA")
	t.Setenv("LOCAL_DEV", "true")
	t.Setenv("POLL_INTERVAL", "30s")
	t.Setenv("LOOKBACK_WINDOW", "90s")
	// JIRA_BASE_URL intentionally absent.

	_, err := config.Load()
	if !errors.Is(err, config.ErrConfigMissing) {
		t.Fatalf("err = %v, want ErrConfigMissing", err)
	}
	if !strContains(err.Error(), "JIRA_BASE_URL") {
		t.Errorf("error should mention JIRA_BASE_URL: %v", err)
	}
}

// CT-2: LookbackWindow <= PollInterval → ErrConfigInvalid.
func TestLoad_CT2_LookbackNotGreaterThanPoll(t *testing.T) {
	clearEnv(t)
	t.Setenv("JIRA_BASE_URL", "https://jira.example.com")
	t.Setenv("JIRA_PROJECTS", "INFRA")
	t.Setenv("LOCAL_DEV", "true")
	t.Setenv("POLL_INTERVAL", "60s")
	t.Setenv("LOOKBACK_WINDOW", "60s") // equal — not strictly greater

	_, err := config.Load()
	if !errors.Is(err, config.ErrConfigInvalid) {
		t.Fatalf("err = %v, want ErrConfigInvalid", err)
	}
}

// CT-3: YAML values are overridden by env vars (env always wins).
func TestLoad_CT3_EnvOverridesYAML(t *testing.T) {
	clearEnv(t)

	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "config.yaml")
	// YAML sets poll_interval to 30s; env will override to 90s.
	if err := os.WriteFile(yamlPath, []byte("poll_interval: 30s\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	t.Setenv("CONFIG_FILE", yamlPath)
	t.Setenv("POLL_INTERVAL", "90s") // env wins
	t.Setenv("JIRA_BASE_URL", "https://jira.example.com")
	t.Setenv("JIRA_PROJECTS", "INFRA")
	t.Setenv("LOCAL_DEV", "true")
	t.Setenv("LOOKBACK_WINDOW", "300s")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PollInterval != 90*time.Second {
		t.Errorf("PollInterval = %s, want 90s (env should override YAML 30s)", cfg.PollInterval)
	}
}

// Validate: JIRA_PROJECTS missing → ErrConfigMissing.
func TestLoad_MissingProjects(t *testing.T) {
	clearEnv(t)
	t.Setenv("JIRA_BASE_URL", "https://jira.example.com")
	t.Setenv("LOCAL_DEV", "true")
	t.Setenv("POLL_INTERVAL", "30s")
	t.Setenv("LOOKBACK_WINDOW", "90s")

	_, err := config.Load()
	if !errors.Is(err, config.ErrConfigMissing) {
		t.Fatalf("err = %v, want ErrConfigMissing", err)
	}
}

// Validate: SECRET_ARN missing when LOCAL_DEV=false → ErrConfigMissing.
func TestLoad_MissingSecretARN_ProdMode(t *testing.T) {
	clearEnv(t)
	t.Setenv("JIRA_BASE_URL", "https://jira.example.com")
	t.Setenv("JIRA_PROJECTS", "INFRA")
	t.Setenv("POLL_INTERVAL", "30s")
	t.Setenv("LOOKBACK_WINDOW", "90s")
	// LOCAL_DEV not set → production mode → SECRET_ARN required.

	_, err := config.Load()
	if !errors.Is(err, config.ErrConfigMissing) {
		t.Fatalf("err = %v, want ErrConfigMissing", err)
	}
}

// Validate: JIRA_PROJECTS parsed from comma-separated string.
func TestLoad_ProjectsCommaSeparated(t *testing.T) {
	clearEnv(t)
	setMinimalValid(t)
	t.Setenv("JIRA_PROJECTS", "INFRA, PLAT, OPS")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.JiraProjects) != 3 {
		t.Errorf("JiraProjects = %v, want [INFRA PLAT OPS]", cfg.JiraProjects)
	}
}

// Validate: defaults applied when optional fields absent.
func TestLoad_Defaults(t *testing.T) {
	clearEnv(t)
	setMinimalValid(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogFormat != "json" {
		t.Errorf("LogFormat = %q, want json", cfg.LogFormat)
	}
	if cfg.CredentialTTL != 6*time.Hour {
		t.Errorf("CredentialTTL = %s, want 6h", cfg.CredentialTTL)
	}
}

func strContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
