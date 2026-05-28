package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileConfig is the top-level YAML config structure.
type FileConfig struct {
	Defaults   DefaultsConfig           `yaml:"defaults"`
	Atlassian  AtlassianConfig          `yaml:"atlassian"`
	GitHub     GitHubConfig             `yaml:"github"`
	Workspace  *WorkspaceConfig         `yaml:"workspace"`
	CareerLens *CareerLensConfig        `yaml:"career_lens"`
	Profiles   map[string]ProfileConfig `yaml:"profiles"`
}

// WorkspaceConfig holds settings for the workspace init command.
type WorkspaceConfig struct {
	OrgPath      string               `yaml:"org_path"`
	WorkspaceDir string               `yaml:"workspace_dir"`
	GitCacheDir  string               `yaml:"git_cache_dir"`
	Jira         *WorkspaceJiraConfig `yaml:"jira"`
}

// WorkspaceJiraConfig holds Jira-specific workspace settings.
type WorkspaceJiraConfig struct {
	DefaultRepos  []string `yaml:"default_repos"`
	BranchPattern string   `yaml:"branch_pattern"`
}

// DefaultsConfig holds default values applied to all runs.
type DefaultsConfig struct {
	Email              string       `yaml:"email"`
	OutputDir          string       `yaml:"output_dir"`
	TimeZone           string       `yaml:"timezone"`
	Format             string       `yaml:"format"`
	Summary            *bool        `yaml:"summary"`
	Debug              *bool        `yaml:"debug"`
	Jira               *bool        `yaml:"jira"`
	Confluence         *bool        `yaml:"confluence"`
	GitHubEnabled      *bool        `yaml:"github"`
	ConfluenceType     string       `yaml:"confluence_type"`
	ConfluenceHydrate  *bool        `yaml:"confluence_hydrate"`
	GitHubAPIStrategy  string       `yaml:"github_api"`
	GitHubRepos        string       `yaml:"github_repos"`
	GitHubEnrich       *bool        `yaml:"github_enrich"`
	Cache              *CacheConfig `yaml:"cache"`
	ConfluenceSpaceKey string       `yaml:"confluence_space_key"`
	ConfluenceFolderID string       `yaml:"confluence_folder_id"`
	StandupAuthor      string       `yaml:"standup_author"`
}

// CacheConfig holds cache settings from the config file.
type CacheConfig struct {
	Enabled *bool           `yaml:"enabled"`
	TTL     *CacheTTLConfig `yaml:"ttl"`
}

// CacheTTLConfig holds per-source TTL overrides as duration strings (e.g. "1h", "15m").
type CacheTTLConfig struct {
	Jira          string `yaml:"jira"`
	Confluence    string `yaml:"confluence"`
	GitHubEvents  string `yaml:"github_events"`
	GitHubSearch  string `yaml:"github_search"`
	GitHubGraphQL string `yaml:"github_graphql"`
}

// CareerLensConfig holds career track scoring configuration.
type CareerLensConfig struct {
	Tracks   map[string]TrackConfig `yaml:"tracks"`
	Ceilings map[string]float64     `yaml:"ceilings"`
}

// TrackConfig defines a single career track with weighted dimensions.
type TrackConfig struct {
	Description string             `yaml:"description"`
	Inherit     string             `yaml:"inherit,omitempty"`
	Weights     map[string]float64 `yaml:"weights"`
}

// AtlassianConfig holds Atlassian connection settings.
type AtlassianConfig struct {
	Domain string `yaml:"domain"`
	Email  string `yaml:"email"`
	Token  string `yaml:"token"`
}

// GitHubConfig holds GitHub connection settings.
type GitHubConfig struct {
	Token string `yaml:"token"`
	User  string `yaml:"user"`
}

// ProfileConfig holds per-profile overrides. Pointer fields mean nil = "not set".
type ProfileConfig struct {
	Email             *string  `yaml:"email"`
	ProjectKeys       *string  `yaml:"project_keys"`
	SpaceKeys         *string  `yaml:"space_keys"`
	GitHubUser        *string  `yaml:"github_user"`
	GitHubAPIStrategy *string  `yaml:"github_api"`
	GitHubRepos       *string  `yaml:"github_repos"`
	GitHubEnrich      *bool    `yaml:"github_enrich"`
	StartDate         *string  `yaml:"start"`
	EndDate           *string  `yaml:"end"`
	Since             *string  `yaml:"since"`
	TimeZone          *string  `yaml:"timezone"`
	OutputDir         *string  `yaml:"output_dir"`
	JiraOutput        *string  `yaml:"jira_output"`
	ConfluenceOutput  *string  `yaml:"confluence_output"`
	GitHubOutput      *string  `yaml:"github_output"`
	Format            *string  `yaml:"format"`
	Summary           *bool    `yaml:"summary"`
	Debug             *bool    `yaml:"debug"`
	Jira              *bool    `yaml:"jira"`
	Confluence        *bool    `yaml:"confluence"`
	GitHubEnabled     *bool    `yaml:"github"`
	ConfluenceType    *string  `yaml:"confluence_type"`
	ConfluenceHydrate *bool    `yaml:"confluence_hydrate"`
	JiraStatus        *string  `yaml:"jira_status"`
	JiraType          *string  `yaml:"jira_type"`
	JiraPriority      *string  `yaml:"jira_priority"`
	Description       *string  `yaml:"description"`
	Inherit           []string `yaml:"inherit"`
}

// DiscoverConfigFile finds the config file using the search order:
// 1. explicit path (--config flag)
// 2. ./.workctl.yaml
// 3. ~/.config/workctl/config.yaml
// Returns "" if no config file is found (not an error).
func DiscoverConfigFile(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("config file not found: %s", explicit)
		}
		return explicit, nil
	}

	// Check ./.workctl.yaml
	if _, err := os.Stat(".workctl.yaml"); err == nil {
		return ".workctl.yaml", nil
	}

	// Check $XDG_CONFIG_HOME/workctl/config.yaml
	xdg := filepath.Join(XDGConfigHome(), "workctl", "config.yaml")
	if _, err := os.Stat(xdg); err == nil {
		return xdg, nil
	}

	return "", nil
}

// LoadConfigFile reads and parses a YAML config file.
// Environment variables in the format ${VAR} are expanded before parsing.
func LoadConfigFile(path string) (*FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	expanded := ExpandEnvVars(string(data))

	var cfg FileConfig
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	return &cfg, nil
}

// envVarPattern matches ${VAR_NAME} patterns.
var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// LoadConfigFileRaw reads and parses a YAML config file WITHOUT expanding
// environment variables. This preserves ${VAR} references so callers can
// detect plaintext secrets that should use env-var syntax.
func LoadConfigFileRaw(path string) (*FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg FileConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	return &cfg, nil
}

// CheckPlaintextTokens inspects a raw (unexpanded) config for token fields
// that contain plaintext values instead of ${VAR} env-var references.
// Returns a warning string for each plaintext token found.
func CheckPlaintextTokens(raw *FileConfig) []string {
	var warnings []string
	type tokenField struct {
		label   string
		value   string
		envHint string
	}
	fields := []tokenField{
		{"atlassian.token", raw.Atlassian.Token, "ATLASSIAN_API_TOKEN"},
		{"github.token", raw.GitHub.Token, "GITHUB_TOKEN"},
	}
	for _, f := range fields {
		if f.value != "" && !strings.HasPrefix(f.value, "${") {
			warnings = append(warnings, fmt.Sprintf(
				"%s appears to contain a plaintext token. Consider using ${%s} instead.",
				f.label, f.envHint,
			))
		}
	}
	return warnings
}

// ExpandEnvVars replaces ${VAR} patterns with environment variable values.
// Only ${BRACED} syntax is expanded; bare $VAR is left as-is.
// Unset variables expand to empty string.
func ExpandEnvVars(s string) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		// Extract variable name from ${VAR}
		name := strings.TrimPrefix(match, "${")
		name = strings.TrimSuffix(name, "}")
		return os.Getenv(name)
	})
}
