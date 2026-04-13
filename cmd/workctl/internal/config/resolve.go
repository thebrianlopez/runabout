package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/models"
)

// ResolvedConfig is the unified config after merging defaults, file, profile, and flags.
type ResolvedConfig struct {
	// Query params
	Email             string
	ProjectKeys       []string
	SpaceKeys         []string
	GitHubUser        string
	GitHubAPIStrategy string
	StartDate         string
	EndDate           string
	TimeZone          string

	// Output paths
	OutputDir        string
	JiraOutput       string
	ConfluenceOutput string
	GitHubOutput     string
	Format           string

	// Enable flags
	Jira       bool
	Confluence bool
	GitHub     bool
	Summary    bool
	Debug      bool

	// Filters
	JiraStatus        []string
	JiraType          []string
	JiraPriority      []string
	ConfluenceType    string
	ConfluenceHydrate bool

	// Atlassian connection (from file config)
	AtlassianDomain string
	AtlassianEmail  string
	AtlassianToken  string

	// GitHub connection (from file config)
	GitHubToken string

	// GitHub commit history
	GitHubRepos  []string
	GitHubEnrich bool

	// Cache settings
	CacheEnabled bool
	CacheTTL     map[string]time.Duration // per-source TTL overrides

	// Workspace settings
	WorkspaceOrgPath       string
	WorkspaceDir           string
	WorkspaceGitCacheDir   string
	WorkspaceDefaultRepos  []string
	WorkspaceBranchPattern string

	// Standup publisher settings (EPIC-014)
	PublishConfluence  bool
	ConfluenceSpaceKey string
	ConfluenceFolderID string
	StandupAuthor      string
	StandupNotesFile   string
	StandupDryRun      bool

	// Local activity sources (EPIC-015)
	Shell   bool // Enable fish history + audit log (default: true)
	AIStats bool // Enable Claude stats cache (default: true)

	// Privacy (H2)
	RedactOthers bool // Redact third-party names/emails from report output (default: false)
}

// FlagValues holds CLI flag values with Set indicators (from Cobra's Changed()).
type FlagValues struct {
	Email             string
	EmailSet          bool
	ProjectKeys       string
	ProjectKeysSet    bool
	SpaceKeys         string
	SpaceKeysSet      bool
	GitHubUser        string
	GitHubUserSet     bool
	GitHubAPIStrategy string
	GitHubAPISet      bool
	StartDate         string
	StartDateSet      bool
	EndDate           string
	EndDateSet        bool
	TimeZone          string
	TimeZoneSet       bool
	JiraOutput        string
	JiraOutputSet     bool
	ConfluenceOutput  string
	ConfOutputSet     bool
	GitHubOutput      string
	GitHubOutputSet   bool
	Format            string
	FormatSet         bool
	Summary           bool
	SummarySet        bool
	Debug             bool
	DebugSet          bool
	Jira              bool
	JiraSet           bool
	Confluence        bool
	ConfluenceSet     bool
	GitHub            bool
	GitHubSet         bool
	JiraStatus        string
	JiraStatusSet     bool
	JiraType          string
	JiraTypeSet       bool
	JiraPriority      string
	JiraPrioritySet   bool
	ConfluenceType    string
	ConfTypeSet       bool
	ConfluenceHydrate bool
	ConfHydrateSet    bool
	GitHubRepos       string
	GitHubReposSet    bool
	GitHubEnrich      bool
	GitHubEnrichSet   bool
	CacheTTLOverride  string // e.g. "4h" — applies to all sources
	CacheTTLSet       bool
	Shell             bool
	ShellSet          bool
	AIStats           bool
	AIStatsSet        bool
	RedactOthers      bool
	RedactOthersSet   bool
}

// Resolve merges configuration from 4 layers (lowest to highest priority):
// 1. Hardcoded defaults
// 2. YAML file defaults + atlassian + github sections
// 3. Named profile (if --profile specified)
// 4. CLI flags (where Set == true)
func Resolve(file *FileConfig, profileName string, flags *FlagValues) (*ResolvedConfig, error) {
	// Layer 1: Hardcoded defaults (XDG-compliant paths)
	outDir := DefaultOutputDir()
	rc := &ResolvedConfig{
		StartDate:         "2025-01-01",
		EndDate:           "2025-02-20",
		TimeZone:          "America/Chicago",
		OutputDir:         outDir,
		JiraOutput:        outDir + "/jira.json",
		ConfluenceOutput:  outDir + "/confluence.json",
		GitHubOutput:      outDir + "/github.json",
		Format:            "json",
		Jira:              true,
		Confluence:        true,
		GitHub:            true,
		ConfluenceType:    "page",
		GitHubAPIStrategy: "auto",
		CacheEnabled:      true,

		// Local activity sources default on
		Shell:   true,
		AIStats: true,

		// Workspace defaults
		WorkspaceDir:           "jira_work",
		WorkspaceGitCacheDir:   ".git_cache",
		WorkspaceDefaultRepos:  []string{"infra-terraform", "infra-helm"},
		WorkspaceBranchPattern: "{key}",
	}

	// Layer 2: YAML file defaults + connection settings
	if file != nil {
		applyFileDefaults(rc, &file.Defaults)
		applyAtlassianConfig(rc, &file.Atlassian)
		applyGitHubConfig(rc, &file.GitHub)
		if file.Workspace != nil {
			applyWorkspaceConfig(rc, file.Workspace)
		}
	}

	// Layer 3: Named profile
	if profileName != "" {
		if file == nil {
			return nil, fmt.Errorf("--profile %q specified but no config file found", profileName)
		}
		profile, ok := file.Profiles[profileName]
		if !ok {
			available := make([]string, 0, len(file.Profiles))
			for name := range file.Profiles {
				available = append(available, name)
			}
			return nil, fmt.Errorf("unknown profile %q (available: %s)", profileName, strings.Join(available, ", "))
		}
		if err := applyProfile(rc, &profile); err != nil {
			return nil, fmt.Errorf("profile %q: %w", profileName, err)
		}
	}

	// Layer 4: CLI flags (always win when explicitly set)
	applyFlags(rc, flags)

	// Validate repo list format (format-only, no network)
	if err := ValidateGitHubRepos(rc.GitHubRepos); err != nil {
		return nil, err
	}

	// Sanitize output paths to prevent ".." traversal.
	rc.JiraOutput = filepath.Clean(rc.JiraOutput)
	rc.ConfluenceOutput = filepath.Clean(rc.ConfluenceOutput)
	rc.GitHubOutput = filepath.Clean(rc.GitHubOutput)

	// Rebuild output paths if output dir changed but individual paths weren't set
	if flags != nil {
		if !flags.JiraOutputSet && rc.OutputDir != outDir {
			rc.JiraOutput = rc.OutputDir + "/jira.json"
		}
		if !flags.ConfOutputSet && rc.OutputDir != outDir {
			rc.ConfluenceOutput = rc.OutputDir + "/confluence.json"
		}
		if !flags.GitHubOutputSet && rc.OutputDir != outDir {
			rc.GitHubOutput = rc.OutputDir + "/github.json"
		}
	}

	return rc, nil
}

func applyFileDefaults(rc *ResolvedConfig, d *DefaultsConfig) {
	if d.Email != "" {
		rc.Email = d.Email
	}
	if d.TimeZone != "" {
		rc.TimeZone = d.TimeZone
	}
	if d.OutputDir != "" {
		rc.OutputDir = d.OutputDir
		rc.JiraOutput = d.OutputDir + "/jira.json"
		rc.ConfluenceOutput = d.OutputDir + "/confluence.json"
		rc.GitHubOutput = d.OutputDir + "/github.json"
	}
	if d.Format != "" {
		rc.Format = d.Format
	}
	if d.Summary != nil {
		rc.Summary = *d.Summary
	}
	if d.Debug != nil {
		rc.Debug = *d.Debug
	}
	if d.Jira != nil {
		rc.Jira = *d.Jira
	}
	if d.Confluence != nil {
		rc.Confluence = *d.Confluence
	}
	if d.GitHubEnabled != nil {
		rc.GitHub = *d.GitHubEnabled
	}
	if d.ConfluenceType != "" {
		rc.ConfluenceType = d.ConfluenceType
	}
	if d.ConfluenceHydrate != nil {
		rc.ConfluenceHydrate = *d.ConfluenceHydrate
	}
	if d.GitHubAPIStrategy != "" {
		rc.GitHubAPIStrategy = d.GitHubAPIStrategy
	}
	if d.GitHubRepos != "" {
		rc.GitHubRepos = ParseCSV(d.GitHubRepos)
	}
	if d.GitHubEnrich != nil {
		rc.GitHubEnrich = *d.GitHubEnrich
	}
	if d.Cache != nil {
		applyCacheConfig(rc, d.Cache)
	}
	if d.ConfluenceSpaceKey != "" {
		rc.ConfluenceSpaceKey = d.ConfluenceSpaceKey
	}
	if d.ConfluenceFolderID != "" {
		rc.ConfluenceFolderID = d.ConfluenceFolderID
	}
	if d.StandupAuthor != "" {
		rc.StandupAuthor = d.StandupAuthor
	}
}

func applyCacheConfig(rc *ResolvedConfig, c *CacheConfig) {
	if c.Enabled != nil {
		rc.CacheEnabled = *c.Enabled
	}
	if c.TTL != nil {
		if rc.CacheTTL == nil {
			rc.CacheTTL = make(map[string]time.Duration)
		}
		parseTTL := func(source, raw string) {
			if raw == "" {
				return
			}
			d, err := time.ParseDuration(raw)
			if err != nil {
				return // silently ignore invalid durations
			}
			rc.CacheTTL[source] = d
		}
		parseTTL("jira", c.TTL.Jira)
		parseTTL("confluence", c.TTL.Confluence)
		parseTTL("github_events", c.TTL.GitHubEvents)
		parseTTL("github_search", c.TTL.GitHubSearch)
		parseTTL("github_graphql", c.TTL.GitHubGraphQL)
	}
}

func applyAtlassianConfig(rc *ResolvedConfig, a *AtlassianConfig) {
	if a.Domain != "" {
		rc.AtlassianDomain = a.Domain
	}
	if a.Email != "" {
		rc.AtlassianEmail = a.Email
	}
	if a.Token != "" {
		rc.AtlassianToken = a.Token
	}
}

func applyGitHubConfig(rc *ResolvedConfig, g *GitHubConfig) {
	if g.Token != "" {
		rc.GitHubToken = g.Token
	}
	if g.User != "" {
		rc.GitHubUser = g.User
	}
}

func applyWorkspaceConfig(rc *ResolvedConfig, w *WorkspaceConfig) {
	if w.OrgPath != "" {
		rc.WorkspaceOrgPath = w.OrgPath
	}
	if w.WorkspaceDir != "" {
		rc.WorkspaceDir = w.WorkspaceDir
	}
	if w.GitCacheDir != "" {
		rc.WorkspaceGitCacheDir = w.GitCacheDir
	}
	if w.Jira != nil {
		if len(w.Jira.DefaultRepos) > 0 {
			rc.WorkspaceDefaultRepos = w.Jira.DefaultRepos
		}
		if w.Jira.BranchPattern != "" {
			rc.WorkspaceBranchPattern = w.Jira.BranchPattern
		}
	}
}

func applyProfile(rc *ResolvedConfig, p *ProfileConfig) error {
	if p.Email != nil {
		rc.Email = *p.Email
	}
	if p.ProjectKeys != nil {
		rc.ProjectKeys = ParseCSV(*p.ProjectKeys)
	}
	if p.SpaceKeys != nil {
		rc.SpaceKeys = ParseCSV(*p.SpaceKeys)
	}
	if p.GitHubUser != nil {
		rc.GitHubUser = *p.GitHubUser
	}
	if p.GitHubAPIStrategy != nil {
		rc.GitHubAPIStrategy = *p.GitHubAPIStrategy
	}
	if p.GitHubRepos != nil {
		rc.GitHubRepos = ParseCSV(*p.GitHubRepos)
	}
	if p.GitHubEnrich != nil {
		rc.GitHubEnrich = *p.GitHubEnrich
	}
	if p.Since != nil {
		start, end, err := ParseSince(*p.Since)
		if err != nil {
			return fmt.Errorf("invalid since value: %w", err)
		}
		rc.StartDate = start
		rc.EndDate = end
	}
	if p.StartDate != nil {
		rc.StartDate = *p.StartDate
	}
	if p.EndDate != nil {
		rc.EndDate = *p.EndDate
	}
	if p.TimeZone != nil {
		rc.TimeZone = *p.TimeZone
	}
	if p.OutputDir != nil {
		rc.OutputDir = *p.OutputDir
		rc.JiraOutput = *p.OutputDir + "/jira.json"
		rc.ConfluenceOutput = *p.OutputDir + "/confluence.json"
		rc.GitHubOutput = *p.OutputDir + "/github.json"
	}
	if p.JiraOutput != nil {
		rc.JiraOutput = *p.JiraOutput
	}
	if p.ConfluenceOutput != nil {
		rc.ConfluenceOutput = *p.ConfluenceOutput
	}
	if p.GitHubOutput != nil {
		rc.GitHubOutput = *p.GitHubOutput
	}
	if p.Format != nil {
		rc.Format = *p.Format
	}
	if p.Summary != nil {
		rc.Summary = *p.Summary
	}
	if p.Debug != nil {
		rc.Debug = *p.Debug
	}
	if p.Jira != nil {
		rc.Jira = *p.Jira
	}
	if p.Confluence != nil {
		rc.Confluence = *p.Confluence
	}
	if p.GitHubEnabled != nil {
		rc.GitHub = *p.GitHubEnabled
	}
	if p.ConfluenceType != nil {
		rc.ConfluenceType = *p.ConfluenceType
	}
	if p.ConfluenceHydrate != nil {
		rc.ConfluenceHydrate = *p.ConfluenceHydrate
	}
	if p.JiraStatus != nil {
		rc.JiraStatus = ParseCSV(*p.JiraStatus)
	}
	if p.JiraType != nil {
		rc.JiraType = ParseCSV(*p.JiraType)
	}
	if p.JiraPriority != nil {
		rc.JiraPriority = ParseCSV(*p.JiraPriority)
	}
	return nil
}

func applyFlags(rc *ResolvedConfig, f *FlagValues) {
	if f == nil {
		return
	}
	if f.EmailSet {
		rc.Email = f.Email
	}
	if f.ProjectKeysSet {
		rc.ProjectKeys = ParseCSV(f.ProjectKeys)
	}
	if f.SpaceKeysSet {
		rc.SpaceKeys = ParseCSV(f.SpaceKeys)
	}
	if f.GitHubUserSet {
		rc.GitHubUser = f.GitHubUser
	}
	if f.GitHubAPISet {
		rc.GitHubAPIStrategy = f.GitHubAPIStrategy
	}
	if f.StartDateSet {
		rc.StartDate = f.StartDate
	}
	if f.EndDateSet {
		rc.EndDate = f.EndDate
	}
	if f.TimeZoneSet {
		rc.TimeZone = f.TimeZone
	}
	if f.JiraOutputSet {
		rc.JiraOutput = f.JiraOutput
	}
	if f.ConfOutputSet {
		rc.ConfluenceOutput = f.ConfluenceOutput
	}
	if f.GitHubOutputSet {
		rc.GitHubOutput = f.GitHubOutput
	}
	if f.FormatSet {
		rc.Format = f.Format
	}
	if f.SummarySet {
		rc.Summary = f.Summary
	}
	if f.DebugSet {
		rc.Debug = f.Debug
	}
	if f.JiraSet {
		rc.Jira = f.Jira
	}
	if f.ConfluenceSet {
		rc.Confluence = f.Confluence
	}
	if f.GitHubSet {
		rc.GitHub = f.GitHub
	}
	if f.JiraStatusSet {
		rc.JiraStatus = ParseCSV(f.JiraStatus)
	}
	if f.JiraTypeSet {
		rc.JiraType = ParseCSV(f.JiraType)
	}
	if f.JiraPrioritySet {
		rc.JiraPriority = ParseCSV(f.JiraPriority)
	}
	if f.ConfTypeSet {
		rc.ConfluenceType = f.ConfluenceType
	}
	if f.ConfHydrateSet {
		rc.ConfluenceHydrate = f.ConfluenceHydrate
	}
	if f.GitHubReposSet {
		rc.GitHubRepos = ParseCSV(f.GitHubRepos)
	}
	if f.GitHubEnrichSet {
		rc.GitHubEnrich = f.GitHubEnrich
	}
	if f.ShellSet {
		rc.Shell = f.Shell
	}
	if f.AIStatsSet {
		rc.AIStats = f.AIStats
	}
	if f.RedactOthersSet {
		rc.RedactOthers = f.RedactOthers
	}
	if f.CacheTTLSet {
		d, err := time.ParseDuration(f.CacheTTLOverride)
		if err != nil {
			// Try flexible parsing for "0" (equivalent to --no-cache)
			if f.CacheTTLOverride == "0" {
				d = 0
			} else {
				LogDebug("invalid --cache-ttl value %q: %v (ignored)", f.CacheTTLOverride, err)
				return
			}
		}
		if d == 0 {
			rc.CacheEnabled = false
		} else {
			if rc.CacheTTL == nil {
				rc.CacheTTL = make(map[string]time.Duration)
			}
			for _, src := range []string{"jira", "confluence", "github_events", "github_search", "github_graphql"} {
				rc.CacheTTL[src] = d
			}
		}
	}
}

// ParseSince converts a relative duration like "7d", "2w", "1m", "1y" to absolute start/end dates.
// The end date is always today. The start date is computed by subtracting the duration.
func ParseSince(since string) (start, end string, err error) {
	if len(since) < 2 {
		return "", "", fmt.Errorf("invalid since format: %q (expected e.g. 7d, 2w, 1m, 1y)", since)
	}

	numStr := since[:len(since)-1]
	unit := since[len(since)-1]

	var n int
	if _, err := fmt.Sscanf(numStr, "%d", &n); err != nil || n <= 0 {
		return "", "", fmt.Errorf("invalid since format: %q (number must be positive integer)", since)
	}

	now := time.Now()
	end = now.Format("2006-01-02")

	var startTime time.Time
	switch unit {
	case 'd':
		startTime = now.AddDate(0, 0, -n)
	case 'w':
		startTime = now.AddDate(0, 0, -n*7)
	case 'm':
		startTime = now.AddDate(0, -n, 0)
	case 'y':
		startTime = now.AddDate(-n, 0, 0)
	default:
		return "", "", fmt.Errorf("invalid since unit %q (expected d, w, m, or y)", string(unit))
	}

	start = startTime.Format("2006-01-02")
	return start, end, nil
}

// ToQueryConfig converts a ResolvedConfig to a QueryConfig for backward compatibility with API layer.
func ToQueryConfig(rc *ResolvedConfig) (*models.QueryConfig, error) {
	mode, err := DetermineQueryMode(rc.Email, strings.Join(rc.ProjectKeys, ","), strings.Join(rc.SpaceKeys, ","), rc.GitHubUser)
	if err != nil {
		return nil, err
	}

	return &models.QueryConfig{
		Mode:              mode,
		Email:             rc.Email,
		ProjectKeys:       rc.ProjectKeys,
		SpaceKeys:         rc.SpaceKeys,
		GitHubUser:        rc.GitHubUser,
		GitHubAPIStrategy: rc.GitHubAPIStrategy,
		StartDate:         rc.StartDate,
		EndDate:           rc.EndDate,
		TimeZone:          rc.TimeZone,
		Debug:             rc.Debug,
		JiraStatus:        rc.JiraStatus,
		JiraType:          rc.JiraType,
		JiraPriority:      rc.JiraPriority,
		ConfluenceType:    rc.ConfluenceType,
		ConfluenceHydrate: rc.ConfluenceHydrate,
		GitHubRepos:       rc.GitHubRepos,
		GitHubEnrich:      rc.GitHubEnrich,
		Summary:           rc.Summary,
		OutputFormat:      rc.Format,
	}, nil
}
