package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/config"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/ui"
)

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage workctl configuration",
	}
	cmd.AddCommand(configValidateCmd())
	cmd.AddCommand(configShowCmd())
	return cmd
}

func configValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate the config file",
		RunE:  runConfigValidate,
	}
}

func configShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show resolved configuration",
		RunE:  runConfigShow,
	}
}

var kebabCase = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func runConfigValidate(cmd *cobra.Command, args []string) error {
	if fileConfig == nil {
		return fmt.Errorf("no config file found (run 'workctl init' to create one)")
	}

	errors := 0
	warnings := 0

	ui.Infof("Validating config...\n")

	// Validate profile names are kebab-case
	for name := range fileConfig.Profiles {
		if !kebabCase.MatchString(name) {
			ui.Errorf("  ERROR: profile name %q is not kebab-case (use lowercase letters, numbers, hyphens)\n", name)
			errors++
		}
	}

	// Validate profile date formats
	for name, p := range fileConfig.Profiles {
		if p.StartDate != nil {
			if _, err := time.Parse("2006-01-02", *p.StartDate); err != nil {
				ui.Errorf("  ERROR: profile %q start date %q is not YYYY-MM-DD\n", name, *p.StartDate)
				errors++
			}
		}
		if p.EndDate != nil {
			if _, err := time.Parse("2006-01-02", *p.EndDate); err != nil {
				ui.Errorf("  ERROR: profile %q end date %q is not YYYY-MM-DD\n", name, *p.EndDate)
				errors++
			}
		}
		if p.Since != nil {
			if _, _, err := config.ParseSince(*p.Since); err != nil {
				ui.Errorf("  ERROR: profile %q since %q is invalid: %v\n", name, *p.Since, err)
				errors++
			}
		}
	}

	// Warn on plaintext tokens and unset env var references.
	// Load the raw (unexpanded) config to distinguish ${VAR} refs from plaintext.
	configPath, _ := cmd.Root().PersistentFlags().GetString("config")
	cfgPath, _ := config.DiscoverConfigFile(configPath)
	if cfgPath != "" {
		if raw, err := config.LoadConfigFileRaw(cfgPath); err == nil {
			for _, w := range config.CheckPlaintextTokens(raw) {
				ui.Warnf("  WARN: %s\n", w)
				warnings++
			}
		}
	}

	// Warn on env var references that are unset
	checkEnvWarning := func(field, envName string) {
		if strings.Contains(field, "${"+envName+"}") && os.Getenv(envName) == "" {
			ui.Warnf("  WARN: %s references ${%s} which is not set\n", field, envName)
			warnings++
		}
	}
	checkEnvWarning("atlassian.token", "ATLASSIAN_API_TOKEN")
	checkEnvWarning("github.token", "GITHUB_TOKEN")

	// Validate timezone if set
	if fileConfig.Defaults.TimeZone != "" {
		if _, err := time.LoadLocation(fileConfig.Defaults.TimeZone); err != nil {
			ui.Errorf("  ERROR: defaults.timezone %q is invalid\n", fileConfig.Defaults.TimeZone)
			errors++
		}
	}

	// Report
	fmt.Printf("\nProfiles: %d\n", len(fileConfig.Profiles))
	if errors == 0 && warnings == 0 {
		ui.Successf("Config is valid.\n")
	} else {
		ui.Infof("Result: %d error(s), %d warning(s)\n", errors, warnings)
	}

	if errors > 0 {
		return fmt.Errorf("config validation failed with %d error(s)", errors)
	}
	return nil
}

func runConfigShow(cmd *cobra.Command, args []string) error {
	if resolved == nil {
		return fmt.Errorf("no resolved config available")
	}

	// Build a display struct with secrets masked
	display := map[string]interface{}{
		"email":              resolved.Email,
		"project_keys":       resolved.ProjectKeys,
		"space_keys":         resolved.SpaceKeys,
		"github_user":        resolved.GitHubUser,
		"github_api":         resolved.GitHubAPIStrategy,
		"start":              resolved.StartDate,
		"end":                resolved.EndDate,
		"timezone":           resolved.TimeZone,
		"output_dir":         resolved.OutputDir,
		"jira_output":        resolved.JiraOutput,
		"conf_output":        resolved.ConfluenceOutput,
		"github_output":      resolved.GitHubOutput,
		"format":             resolved.Format,
		"jira":               resolved.Jira,
		"confluence":         resolved.Confluence,
		"github":             resolved.GitHub,
		"summary":            resolved.Summary,
		"debug":              resolved.Debug,
		"jira_status":        resolved.JiraStatus,
		"jira_type":          resolved.JiraType,
		"jira_priority":      resolved.JiraPriority,
		"confluence_type":    resolved.ConfluenceType,
		"confluence_hydrate": resolved.ConfluenceHydrate,
	}

	// Mask secrets
	if resolved.AtlassianDomain != "" {
		display["atlassian_domain"] = resolved.AtlassianDomain
	}
	if resolved.AtlassianEmail != "" {
		display["atlassian_email"] = resolved.AtlassianEmail
	}
	if resolved.AtlassianToken != "" {
		display["atlassian_token"] = maskSecret(resolved.AtlassianToken)
	}
	if resolved.GitHubToken != "" {
		display["github_token"] = maskSecret(resolved.GitHubToken)
	}

	data, err := yaml.Marshal(display)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	fmt.Println("Resolved Configuration:")
	fmt.Println(strings.Repeat("-", 40))
	fmt.Print(string(data))
	return nil
}

func maskSecret(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:4] + strings.Repeat("*", len(s)-4)
}
