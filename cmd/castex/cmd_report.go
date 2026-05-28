package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/thebrianlopez/runabout/internal/registry"
)

func newReportCmd() *cobra.Command {
	var eventsDir string
	var orgYAMLPath string
	var localOverridePath string

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Classify and group AI usage events by agent archetype, model tier, and provider",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReport(cmd, eventsDir, orgYAMLPath, localOverridePath)
		},
	}

	home, _ := os.UserHomeDir()
	cmd.Flags().StringVar(&eventsDir, "events-dir", filepath.Join(home, ".automation-metrics/events"), "directory containing *.jsonl event files")
	cmd.Flags().StringVar(&orgYAMLPath, "org-yaml", filepath.Join(home, "code/personal/docs/org.yaml"), "path to org.yaml v2.0")
	cmd.Flags().StringVar(&localOverridePath, "local-overrides", filepath.Join(home, ".castex/taxonomy.local.yaml"), "path to taxonomy.local.yaml overrides")

	return cmd
}

type eventLine struct {
	AgentTool string `json:"agent_tool"`
}

type agentStats struct {
	agentID        string
	classification registry.Classification
	count          int
	unknown        bool
}

func runReport(cmd *cobra.Command, eventsDir, orgYAMLPath, localOverridePath string) error {
	// I301: warn if legacy taxonomy.yaml still present
	home, _ := os.UserHomeDir()
	legacyPath := filepath.Join(home, ".castex/taxonomy.yaml")
	if _, err := os.Stat(legacyPath); err == nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "[I301] Legacy taxonomy.yaml found at %s — no longer used (reading from org.yaml)\n", legacyPath)
	}

	r, err := registry.LoadWithOverrides(orgYAMLPath, localOverridePath)
	if err != nil {
		return fmt.Errorf("loading registry: %w", err)
	}

	counts := make(map[string]int)
	if err := scanEvents(eventsDir, counts); err != nil {
		return fmt.Errorf("scanning events: %w", err)
	}

	if len(counts) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No events found.")
		return nil
	}

	// Classify each unique agent_tool
	var stats []agentStats
	var unknownCount int

	agentIDs := make([]string, 0, len(counts))
	for id := range counts {
		agentIDs = append(agentIDs, id)
	}
	sort.Strings(agentIDs)

	for _, agentID := range agentIDs {
		c, err := r.ClassifyAgent(agentID)
		if err != nil {
			unknownCount += counts[agentID]
			stats = append(stats, agentStats{agentID: agentID, count: counts[agentID], unknown: true})
			continue
		}
		stats = append(stats, agentStats{agentID: agentID, classification: c, count: counts[agentID]})
	}

	// Sort: known agents first (by count desc), then unknown
	sort.SliceStable(stats, func(i, j int) bool {
		if stats[i].unknown != stats[j].unknown {
			return !stats[i].unknown
		}
		return stats[i].count > stats[j].count
	})

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "AGENT\tARCHETYPE\tMODEL TIER\tPROVIDER\tEVENTS")
	fmt.Fprintln(w, "─────\t─────────\t──────────\t────────\t──────")

	for _, s := range stats {
		if s.unknown {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n", s.agentID, "(unregistered)", "-", "-", s.count)
		} else {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n",
				s.agentID, s.classification.Archetype, s.classification.ModelTier,
				s.classification.ProviderType, s.count)
		}
	}
	w.Flush()

	// Summary by archetype
	archGroups := make(map[string]int)
	for _, s := range stats {
		if s.unknown {
			archGroups["unknown"] += s.count
		} else {
			archGroups[s.classification.Archetype] += s.count
		}
	}

	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintln(cmd.OutOrStdout(), "By archetype:")
	archetypes := make([]string, 0, len(archGroups))
	for k := range archGroups {
		archetypes = append(archetypes, k)
	}
	sort.Strings(archetypes)
	for _, arch := range archetypes {
		fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %d\n", arch, archGroups[arch])
	}

	if unknownCount > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "\n[W301] %d events from unregistered agents (see 'unknown' rows above)\n", unknownCount)
	}

	return nil
}

func scanEvents(eventsDir string, counts map[string]int) error {
	pattern := filepath.Join(eventsDir, "*.jsonl")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := scanFile(f, counts); err != nil {
			// Non-fatal: skip unreadable files
			continue
		}
	}
	return nil
}

func scanFile(path string, counts map[string]int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev eventLine
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.AgentTool != "" {
			counts[ev.AgentTool]++
		}
	}
	return scanner.Err()
}
