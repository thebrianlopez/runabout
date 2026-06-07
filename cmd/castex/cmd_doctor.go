package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"
	"github.com/thebrianlopez/runabout/internal/registry"
)

// DoctorConfig holds flags for the doctor command.
type DoctorConfig struct {
	OrgYAML     string
	AgentsFile  string
	Verbose     bool
	StateFile   string // ~/.castex/registry-state.json
	EventBusDir string // ~/.automation-metrics/events
}

// DoctorIssue is one finding from a doctor check.
type DoctorIssue struct {
	Code    string
	Agent   string
	Message string
	Fatal   bool
}

// DoctorReport is the output of a doctor run.
type DoctorReport struct {
	RegistryPath   string
	AgentCount     int
	Errors         []DoctorIssue
	Warnings       []DoctorIssue
	MutationEvents []RegistryMutationEvent // non-empty when org.yaml changed since last run
	NewState       *RegistryState          // updated state to persist; nil when no change detected
}

func newDoctorCmd() *cobra.Command {
	var cfg DoctorConfig

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check registry health: stale agents.jsonl entries, missing CWDs, unclassified agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			home, _ := os.UserHomeDir()
			if cfg.OrgYAML == "" {
				// Derive from WS_ORG_REGISTRY env (set by prompt-context hook) or ORG_PATH.
				if v := os.Getenv("WS_ORG_REGISTRY"); v != "" {
					cfg.OrgYAML = v
				} else if op := os.Getenv("ORG_PATH"); op != "" {
					cfg.OrgYAML = filepath.Join(op, "docs", "org.yaml")
				}
			}
			if cfg.AgentsFile == "" {
				cfg.AgentsFile = filepath.Join(home, ".castex", "agents.jsonl")
			}
			if cfg.StateFile == "" {
				cfg.StateFile = filepath.Join(home, ".castex", "registry-state.json")
			}
			if cfg.EventBusDir == "" {
				cfg.EventBusDir = filepath.Join(home, ".automation-metrics", "events")
			}
			report, err := RunDoctor(cfg)
			if err != nil {
				return err
			}
			// Persist updated registry state and emit mutation events.
			if report.NewState != nil {
				_ = saveRegistryState(cfg.StateFile, *report.NewState)
				_ = writeMutationEvents(cfg.EventBusDir, report.MutationEvents)
			}
			return renderDoctorReport(cmd, cfg, report)
		},
	}

	home, _ := os.UserHomeDir()
	cmd.Flags().StringVar(&cfg.OrgYAML, "org-yaml", "", "path to org.yaml registry (default: $WS_ORG_REGISTRY or $ORG_PATH/docs/org.yaml)")
	cmd.Flags().StringVar(&cfg.AgentsFile, "agents-file", filepath.Join(home, ".castex", "agents.jsonl"), "path to castex agents.jsonl")
	cmd.Flags().BoolVar(&cfg.Verbose, "verbose", false, "show all agents, not just issues")
	return cmd
}

// RunDoctor is the testable entry point for the doctor command.
func RunDoctor(cfg DoctorConfig) (DoctorReport, error) {
	report := DoctorReport{RegistryPath: cfg.OrgYAML}

	if cfg.OrgYAML == "" {
		return report, fmt.Errorf("[E501] registry_not_configured: set --org-yaml, WS_ORG_REGISTRY, or ORG_PATH")
	}
	if _, err := os.Stat(cfg.OrgYAML); os.IsNotExist(err) {
		return report, fmt.Errorf("[E501] registry_missing: %s", cfg.OrgYAML)
	}

	reg, err := registry.Load(cfg.OrgYAML)
	if err != nil {
		return report, fmt.Errorf("[E502] registry_load_failed: %w", err)
	}
	report.AgentCount = reg.AgentCount()

	// Propagate schema validation errors from the registry loader.
	for _, ve := range reg.Errors() {
		issue := DoctorIssue{Code: ve.Code, Message: ve.Message, Fatal: ve.Fatal}
		if ve.Fatal {
			report.Errors = append(report.Errors, issue)
		} else {
			report.Warnings = append(report.Warnings, issue)
		}
	}

	// Check 1: missing CWDs for static agents.
	for _, a := range reg.Agents() {
		if a.IsWorkspaceScoped() {
			continue
		}
		cwd := expandHome(*a.CWD)
		if _, err := os.Stat(cwd); os.IsNotExist(err) {
			report.Errors = append(report.Errors, DoctorIssue{
				Code:    "E503",
				Agent:   a.ID,
				Message: fmt.Sprintf("CWD does not exist: %s", cwd),
				Fatal:   true,
			})
		}
	}

	// Check 2: unclassified agents (missing archetype, model, or provider).
	for _, a := range reg.Agents() {
		if a.Archetype == "" {
			report.Warnings = append(report.Warnings, DoctorIssue{
				Code:    "W504",
				Agent:   a.ID,
				Message: "missing archetype field (expected one of: agentic_coder, shell_assistant, orchestrator, tool_runner)",
			})
		}
		if a.DefaultModel == "" {
			report.Warnings = append(report.Warnings, DoctorIssue{
				Code:    "W504",
				Agent:   a.ID,
				Message: "missing default_model field",
			})
		}
		if a.Provider == "" {
			report.Warnings = append(report.Warnings, DoctorIssue{
				Code:    "W504",
				Agent:   a.ID,
				Message: "missing provider field",
			})
		}
	}

	// Check 3: stale agents.jsonl entries (instrumented agents not in registry).
	staleIDs, err := findStaleAgentIDs(cfg.AgentsFile, reg)
	if err != nil && !os.IsNotExist(err) {
		report.Warnings = append(report.Warnings, DoctorIssue{
			Code:    "W505",
			Message: fmt.Sprintf("could not read agents.jsonl: %v", err),
		})
	}
	for _, id := range staleIDs {
		report.Warnings = append(report.Warnings, DoctorIssue{
			Code:    "W505",
			Agent:   id,
			Message: "in ~/.castex/agents.jsonl but not found in registry; re-run castex init",
		})
	}

	// Check 4: registry mutation detection via SHA256 diff.
	if cfg.StateFile != "" {
		currSHA, shaErr := computeFileSHA256(cfg.OrgYAML)
		if shaErr == nil {
			prev, _ := loadRegistryState(cfg.StateFile)
			if prev.SHA256 != currSHA {
				currIDs := make([]string, 0, len(reg.Agents()))
				archetypeOf := make(map[string]string, len(reg.Agents()))
				for _, a := range reg.Agents() {
					currIDs = append(currIDs, a.ID)
					archetypeOf[a.ID] = a.Archetype
				}
				currIDs = sortedStringSlice(currIDs)
				prevIDs := sortedStringSlice(prev.AgentIDs)
				// Only emit events when prev state exists (first run just seeds state).
				if prev.SHA256 != "" {
					report.MutationEvents = diffRegistryAgents(prevIDs, currIDs, currSHA, archetypeOf)
				}
				report.NewState = &RegistryState{SHA256: currSHA, AgentIDs: currIDs}
			}
		}
	}

	return report, nil
}

// findStaleAgentIDs returns agent IDs present in agents.jsonl but absent in the registry.
func findStaleAgentIDs(agentsFile string, reg *registry.Registry) ([]string, error) {
	f, err := os.Open(agentsFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	seen := map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec AgentRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.AgentID == "" || seen[rec.AgentID] {
			continue
		}
		seen[rec.AgentID] = true
		// No-op here; accumulation happens after the scan loop.
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	var stale []string
	for id := range seen {
		if _, ok := reg.LookupAgent(id); !ok && !isKnownHarnessID(id) {
			stale = append(stale, id)
		}
	}
	sort.Strings(stale)
	return stale, nil
}

// isKnownHarnessID returns true for IDs emitted by castex lifecycle hooks
// that map to vocabulary harness names rather than registry agent IDs.
func isKnownHarnessID(id string) bool {
	switch id {
	case "claude-code", "pi", "codex":
		return true
	}
	return false
}

func renderDoctorReport(cmd *cobra.Command, cfg DoctorConfig, report DoctorReport) error {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "castex doctor\n\n")
	fmt.Fprintf(w, "registry: %s\n", report.RegistryPath)
	fmt.Fprintf(w, "agents loaded: %d\n\n", report.AgentCount)

	if cfg.Verbose {
		// TODO: print all agents with resolution state when --verbose
	}

	if len(report.Errors) == 0 && len(report.Warnings) == 0 {
		fmt.Fprintln(w, "  no issues found")
		return nil
	}

	for _, e := range report.Errors {
		agent := ""
		if e.Agent != "" {
			agent = e.Agent + ": "
		}
		fmt.Fprintf(w, "  [%s] error: %s%s\n", e.Code, agent, e.Message)
	}
	for _, wn := range report.Warnings {
		agent := ""
		if wn.Agent != "" {
			agent = wn.Agent + ": "
		}
		fmt.Fprintf(w, "  [%s] warning: %s%s\n", wn.Code, agent, wn.Message)
	}

	fmt.Fprintln(w)
	errCount := len(report.Errors)
	warnCount := len(report.Warnings)
	switch {
	case errCount > 0 && warnCount > 0:
		fmt.Fprintf(w, "%d error(s), %d warning(s)\n", errCount, warnCount)
	case errCount > 0:
		fmt.Fprintf(w, "%d error(s)\n", errCount)
	default:
		fmt.Fprintf(w, "%d warning(s)\n", warnCount)
	}

	if errCount > 0 {
		return fmt.Errorf("doctor found %d error(s)", errCount)
	}
	return nil
}

// expandHome replaces a leading ~ with the user's home directory.
func expandHome(path string) string {
	if len(path) >= 2 && path[:2] == "~/" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}
