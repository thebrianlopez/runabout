package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Proposal is one pending directive proposal from ~/.automation-metrics/proposals/.
type Proposal struct {
	Fingerprint        string  `json:"fingerprint"`
	SignalID           string  `json:"signal_id"`
	TargetFile         string  `json:"target_file"`
	Tier               string  `json:"tier"`
	Confidence         float64 `json:"confidence"`
	EvidenceWindowDays int     `json:"evidence_window_days"`
	ProposedDiff       string  `json:"proposed_diff"`
	TargetHash         string  `json:"target_hash"`
	Status             string  `json:"status"`
	Title              string  `json:"title"`
	Category           string  `json:"category"`
}

// DirectiveDecisionEvent is the event written to the daily JSONL bus.
type DirectiveDecisionEvent struct {
	SchemaVersion string `json:"schema_version"`
	EventType     string `json:"event_type"`
	Timestamp     string `json:"timestamp"`
	Layer         string `json:"layer"`
	Command       string `json:"command"`
	Fingerprint   string `json:"fingerprint"`
	SignalID      string `json:"signal_id"`
	TargetFile    string `json:"target_file"`
	Tier          string `json:"tier"`
	Decision      string `json:"decision"` // "approved" or "rejected"
	AppliedAt     string `json:"applied_at,omitempty"`
	SnoozedUntil  string `json:"snooze_until"`
	DecidedBy     string `json:"decided_by"`
}

// DirectiveConfig holds flags for the directive command.
type DirectiveConfig struct {
	ListOnly     bool
	Apply        string // fingerprint to apply non-interactively
	Reject       string // fingerprint to reject non-interactively
	ProposalsDir string
	DecisionsDir string
	EventBusDir  string
	Input        io.Reader // injected for testing interactive mode
}

// ApplyResult is the outcome of applying a single proposal.
type ApplyResult struct {
	Fingerprint string
	Applied     bool
	SkipReason  string // "drift", "already_applied", "no_diff"
}

func newDirectiveCmd() *cobra.Command {
	var cfg DirectiveConfig

	cmd := &cobra.Command{
		Use:   "directive",
		Short: "Review, approve, or reject pending CLAUDE.md directive proposals",
		RunE: func(cmd *cobra.Command, args []string) error {
			home, _ := os.UserHomeDir()
			if cfg.ProposalsDir == "" {
				cfg.ProposalsDir = filepath.Join(home, ".automation-metrics", "proposals")
			}
			if cfg.DecisionsDir == "" {
				cfg.DecisionsDir = filepath.Join(home, ".automation-metrics", "decisions")
			}
			if cfg.EventBusDir == "" {
				cfg.EventBusDir = filepath.Join(home, ".automation-metrics", "events")
			}
			if cfg.Input == nil {
				cfg.Input = os.Stdin
			}
			return RunDirective(cmd, cfg)
		},
	}

	home, _ := os.UserHomeDir()
	cmd.Flags().BoolVar(&cfg.ListOnly, "list", false, "list pending proposals without interactive review")
	cmd.Flags().StringVar(&cfg.Apply, "apply", "", "apply a single proposal by fingerprint (non-interactive)")
	cmd.Flags().StringVar(&cfg.Reject, "reject", "", "reject a single proposal by fingerprint (non-interactive)")
	cmd.Flags().StringVar(&cfg.ProposalsDir, "proposals-dir", filepath.Join(home, ".automation-metrics", "proposals"), "path to proposals directory")
	cmd.Flags().StringVar(&cfg.DecisionsDir, "decisions-dir", filepath.Join(home, ".automation-metrics", "decisions"), "path to decisions directory")
	cmd.Flags().StringVar(&cfg.EventBusDir, "event-bus-dir", filepath.Join(home, ".automation-metrics", "events"), "path to daily event bus directory")
	return cmd
}

// RunDirective is the testable entry point for the directive command.
func RunDirective(cmd *cobra.Command, cfg DirectiveConfig) error {
	if _, err := os.Stat(cfg.ProposalsDir); os.IsNotExist(err) {
		return fmt.Errorf("[E304] proposals_dir_missing: %s", cfg.ProposalsDir)
	}

	proposals, err := loadProposals(cfg.ProposalsDir, cfg.DecisionsDir)
	if err != nil {
		return err
	}

	if cfg.Apply != "" {
		return applyByFingerprint(cmd, cfg, proposals, cfg.Apply)
	}
	if cfg.Reject != "" {
		return rejectByFingerprint(cmd, cfg, proposals, cfg.Reject)
	}
	if cfg.ListOnly {
		return listProposals(cmd, proposals)
	}

	return interactiveReview(cmd, cfg, proposals)
}

func loadProposals(proposalsDir, decisionsDir string) ([]Proposal, error) {
	files, err := filepath.Glob(filepath.Join(proposalsDir, "*.jsonl"))
	if err != nil {
		return nil, err
	}

	var proposals []Proposal
	now := time.Now().UTC()

	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range splitLines(data) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var p Proposal
			if err := json.Unmarshal(line, &p); err != nil {
				continue
			}
			if p.Fingerprint == "" {
				continue
			}
			// Filter snoozed proposals.
			if isSnoozed(p.Fingerprint, decisionsDir, now) {
				continue
			}
			proposals = append(proposals, p)
		}
	}
	return proposals, nil
}

// isSnoozed returns true if the proposal has an unexpired snooze_until in decisions/.
func isSnoozed(fingerprint, decisionsDir string, now time.Time) bool {
	decisionPath := filepath.Join(decisionsDir, fingerprint+".json")
	data, err := os.ReadFile(decisionPath)
	if err != nil {
		return false
	}
	var dec struct {
		SnoozedUntil string `json:"snooze_until"`
	}
	if err := json.Unmarshal(data, &dec); err != nil || dec.SnoozedUntil == "" {
		return false
	}
	t, err := time.Parse("20060102T150405Z", dec.SnoozedUntil)
	if err != nil {
		return false
	}
	return now.Before(t)
}

func listProposals(cmd *cobra.Command, proposals []Proposal) error {
	if len(proposals) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No pending proposals.")
		return nil
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "%-12s %-6s %-6s %s\n", "FINGERPRINT", "TIER", "CONF", "TARGET")
	fmt.Fprintf(w, "%-12s %-6s %-6s %s\n", strings.Repeat("-", 12), "------", "------", "------")
	for _, p := range proposals {
		fp := p.Fingerprint
		if len(fp) > 12 {
			fp = fp[:12]
		}
		fmt.Fprintf(w, "%-12s %-6s %-6.2f %s\n", fp, p.Tier, p.Confidence, p.TargetFile)
	}
	return nil
}

func interactiveReview(cmd *cobra.Command, cfg DirectiveConfig, proposals []Proposal) error {
	if len(proposals) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No pending proposals.")
		return nil
	}

	reader := bufio.NewReader(cfg.Input)
	for _, p := range proposals {
		fmt.Fprintf(cmd.OutOrStdout(), "\n--- Proposal %s ---\n", p.Fingerprint)
		fmt.Fprintf(cmd.OutOrStdout(), "Target: %s\n", p.TargetFile)
		fmt.Fprintf(cmd.OutOrStdout(), "Tier: %s | Confidence: %.2f\n", p.Tier, p.Confidence)
		if p.Title != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Title: %s\n", p.Title)
		}
		if p.ProposedDiff != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Diff:\n%s\n", p.ProposedDiff)
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "(no proposed_diff - Phase 1: record decision only)")
		}
		fmt.Fprint(cmd.OutOrStdout(), "[a]pprove / [r]eject / [s]kip / [q]uit: ")

		line, err := reader.ReadString('\n')
		if err != nil {
			return nil
		}
		switch strings.TrimSpace(strings.ToLower(line)) {
		case "a":
			result, err := applyProposal(p, cfg)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "apply error: %v\n", err)
			} else if result.SkipReason != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "skipped (%s)\n", result.SkipReason)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "approved: %s\n", p.Fingerprint)
			}
		case "r":
			if err := writeDecision(cfg, p, "rejected"); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "reject error: %v\n", err)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "rejected: %s (snoozed 14d)\n", p.Fingerprint)
			}
		case "s":
			fmt.Fprintln(cmd.OutOrStdout(), "skipped")
		case "q":
			fmt.Fprintln(cmd.OutOrStdout(), "quit")
			return nil
		default:
			fmt.Fprintln(cmd.OutOrStdout(), "skipped (unrecognized input)")
		}
	}
	return nil
}

func applyByFingerprint(cmd *cobra.Command, cfg DirectiveConfig, proposals []Proposal, fp string) error {
	for _, p := range proposals {
		if p.Fingerprint == fp {
			result, err := applyProposal(p, cfg)
			if err != nil {
				return err
			}
			if result.SkipReason != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "skipped (%s)\n", result.SkipReason)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "approved: %s\n", fp)
			}
			return nil
		}
	}
	return fmt.Errorf("fingerprint not found: %s", fp)
}

func rejectByFingerprint(cmd *cobra.Command, cfg DirectiveConfig, proposals []Proposal, fp string) error {
	for _, p := range proposals {
		if p.Fingerprint == fp {
			return writeDecision(cfg, p, "rejected")
		}
	}
	return fmt.Errorf("fingerprint not found: %s", fp)
}

// applyProposal applies a proposal's proposed_diff to the target file via str.replace.
// If proposed_diff is empty (Phase 1), it records the decision without modifying the file.
func applyProposal(p Proposal, cfg DirectiveConfig) (ApplyResult, error) {
	result := ApplyResult{Fingerprint: p.Fingerprint}

	if p.TargetFile != "" && p.TargetHash != "" {
		// Drift check: verify the target file hasn't changed since the proposal.
		current, err := os.ReadFile(p.TargetFile)
		if err != nil && !os.IsNotExist(err) {
			return result, fmt.Errorf("[E302] write_failed: %w", err)
		}
		if err == nil {
			currentHash := fmt.Sprintf("%x", sha256.Sum256(current))
			if currentHash != p.TargetHash {
				result.SkipReason = "drift"
				return result, fmt.Errorf("[E301] target_file_drift: file changed since proposal; re-run adirective for fresh proposal")
			}
		}
	}

	if p.ProposedDiff != "" && p.TargetFile != "" {
		// Apply the str.replace exactly once.
		current, err := os.ReadFile(p.TargetFile)
		if err != nil {
			return result, fmt.Errorf("[E302] write_failed: %w", err)
		}

		idx := strings.Index(string(current), p.ProposedDiff)
		if idx < 0 {
			result.SkipReason = "drift"
			return result, fmt.Errorf("[E301] target_file_drift: proposed_diff not found in target file")
		}

		// Write to tmpfile then rename for atomicity.
		tmp, err := os.CreateTemp(filepath.Dir(p.TargetFile), ".castex-apply-*")
		if err != nil {
			return result, fmt.Errorf("[E302] write_failed: %w", err)
		}
		tmpPath := tmp.Name()
		_, writeErr := tmp.Write(current)
		tmp.Close()
		if writeErr != nil {
			os.Remove(tmpPath)
			return result, fmt.Errorf("[E302] write_failed: %w", writeErr)
		}
		if err := os.Rename(tmpPath, p.TargetFile); err != nil {
			os.Remove(tmpPath)
			return result, fmt.Errorf("[E302] write_failed: %w", err)
		}
	}

	// Write decision event (non-fatal if it fails).
	if err := writeDecision(cfg, p, "approved"); err != nil {
		fmt.Fprintf(os.Stderr, "[E306] event_bus_write_failed: %v\n", err)
	}

	result.Applied = true
	return result, nil
}

// writeDecision records an approve/reject decision to the decisions dir and the daily event bus.
func writeDecision(cfg DirectiveConfig, p Proposal, decision string) error {
	now := time.Now().UTC()
	snoozedUntil := ""
	if decision == "rejected" {
		snoozedUntil = now.AddDate(0, 0, 14).Format("20060102T150405Z")
	}

	ev := DirectiveDecisionEvent{
		SchemaVersion: "2",
		EventType:     "directive_decision",
		Timestamp:     now.Format("20060102T150405Z"),
		Layer:         "orchestration",
		Command:       "castex directive",
		Fingerprint:   p.Fingerprint,
		SignalID:      p.SignalID,
		TargetFile:    p.TargetFile,
		Tier:          p.Tier,
		Decision:      decision,
		SnoozedUntil:  snoozedUntil,
		DecidedBy:     "brian",
	}

	// Write to decisions/{fingerprint}.json.
	if err := os.MkdirAll(cfg.DecisionsDir, 0o755); err != nil {
		return err
	}
	decPath := filepath.Join(cfg.DecisionsDir, p.Fingerprint+".json")
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if err := os.WriteFile(decPath, b, 0o644); err != nil {
		return err
	}

	// Append to daily event bus.
	if cfg.EventBusDir != "" {
		today := now.Format("2006-01-02")
		busPath := filepath.Join(cfg.EventBusDir, today+".jsonl")
		if err := os.MkdirAll(cfg.EventBusDir, 0o755); err != nil {
			return fmt.Errorf("[E306] event_bus_write_failed: %w", err)
		}
		f, err := os.OpenFile(busPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("[E306] event_bus_write_failed: %w", err)
		}
		defer f.Close()
		if _, err := fmt.Fprintf(f, "%s\n", b); err != nil {
			return fmt.Errorf("[E306] event_bus_write_failed: %w", err)
		}
	}

	return nil
}
