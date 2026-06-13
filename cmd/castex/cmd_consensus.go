package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thebrianlopez/runabout/internal/consensus"
	"github.com/thebrianlopez/runabout/internal/registry"
)

func newConsensusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consensus",
		Short: "L2 agent quorum consensus commands",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newConsensusSubmitCmd())
	cmd.AddCommand(newConsensusVoteCmd())
	cmd.AddCommand(newConsensusStatusCmd())
	cmd.AddCommand(newConsensusAuditCmd())
	return cmd
}

// ─── submit ──────────────────────────────────────────────────────────────────

func newConsensusSubmitCmd() *cobra.Command {
	var (
		agents  []string
		timeout time.Duration
	)

	cmd := &cobra.Command{
		Use:   "submit <artifact_path>",
		Short: "Open a consensus round for an artifact and dispatch vote requests",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConsensusSubmit(cmd.Context(), cmd.OutOrStdout(), args[0], agents, timeout)
		},
	}
	cmd.Flags().StringSliceVar(&agents, "agents", nil, "Comma-separated agent IDs to include (default: all registered)")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "Voting window duration")
	return cmd
}

func runConsensusSubmit(ctx context.Context, out io.Writer, artifactPath string, agentIDs []string, timeout time.Duration) error {
	content, err := os.ReadFile(artifactPath)
	if err != nil {
		return fmt.Errorf("read artifact: %w", err)
	}
	artifactHash := consensus.HashArtifact(content)

	// Load registry to resolve agent CWDs.
	reg, err := loadRegistry()
	if err != nil {
		return fmt.Errorf("load registry: %w", err)
	}

	// Default to all agents if none specified.
	if len(agentIDs) == 0 {
		for _, a := range reg.Agents() {
			agentIDs = append(agentIDs, a.ID)
		}
	}

	roundID := consensus.NewRoundID()
	now := time.Now().UTC()
	expires := now.Add(timeout)
	artifactType := inferArtifactType(artifactPath)

	bus := consensus.NewJSONLEventBus("")
	_ = bus.Append(ctx, "consensus_round_opened", map[string]any{
		"round_id":         roundID,
		"artifact_path":    artifactPath,
		"artifact_hash":    artifactHash,
		"required_agents":  agentIDs,
		"quorum_threshold": 0.51,
		"expires_at":       expires.Format(time.RFC3339),
		"opened_at":        now.Format(time.RFC3339),
	})

	// Write dispatch file for each required agent.
	dispatched := 0
	for _, agentID := range agentIDs {
		agentRec, ok := reg.LookupAgent(agentID)
		if !ok {
			fmt.Fprintf(os.Stderr, "warning: agent %s not in registry; skipping dispatch\n", agentID)
			continue
		}
		cwd := resolveAgentCWD(agentRec)
		if cwd == "" {
			fmt.Fprintf(os.Stderr, "warning: agent %s has no static CWD; skipping dispatch\n", agentID)
			continue
		}

		path, err := consensus.WriteVoteDispatch(cwd, agentID, roundID, artifactPath, artifactHash, artifactType, "promotion", nil, expires)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: dispatch write for %s failed: %v\n", agentID, err)
			continue
		}
		_ = bus.Append(ctx, "consensus_dispatch_written", map[string]any{
			"round_id":      roundID,
			"agent_id":      agentID,
			"dispatch_path": path,
		})
		dispatched++
	}

	fmt.Fprintf(out, "round_id: %s\ndispatched: %d/%d agents\nexpires: %s\n",
		roundID, dispatched, len(agentIDs), expires.Format(time.RFC3339))
	return nil
}

// ─── vote ─────────────────────────────────────────────────────────────────────

func newConsensusVoteCmd() *cobra.Command {
	var (
		roundID    string
		vote       string
		confidence float64
		rationale  string
	)

	cmd := &cobra.Command{
		Use:   "vote",
		Short: "Cast a signed vote on an open consensus round",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConsensusVote(cmd.Context(), cmd.OutOrStdout(), roundID, vote, confidence, rationale)
		},
	}
	cmd.Flags().StringVar(&roundID, "round", "", "Consensus round ID (required)")
	cmd.Flags().StringVar(&vote, "vote", "", "Vote value: approve|reject|abstain (required)")
	cmd.Flags().Float64Var(&confidence, "confidence", 0.85, "Confidence score 0.0-1.0")
	cmd.Flags().StringVar(&rationale, "rationale", "", "Optional rationale text")
	_ = cmd.MarkFlagRequired("round")
	_ = cmd.MarkFlagRequired("vote")
	return cmd
}

func runConsensusVote(ctx context.Context, out io.Writer, roundID, vote string, confidence float64, rationale string) error {
	if vote != "approve" && vote != "reject" && vote != "abstain" {
		return &consensus.QuorumError{
			Code: "CQ-004", Class: "invalid_vote_value",
			Message: "invalid vote: must be approve, reject, or abstain",
		}
	}

	// Look up the round from event bus to validate it's open and get artifact hash.
	bus := consensus.NewJSONLEventBus("")
	events, err := consensus.ScanConsensusEvents("", "consensus_round_opened", "", time.Now().AddDate(-1, 0, 0))
	if err != nil {
		return fmt.Errorf("scan event bus: %w", err)
	}
	_ = bus

	var roundEvent map[string]any
	for _, e := range events {
		if rid, _ := e["round_id"].(string); rid == roundID {
			roundEvent = e
			break
		}
	}
	if roundEvent == nil {
		return &consensus.QuorumError{
			Code: "CQ-001", Class: "round_not_found",
			Message: fmt.Sprintf("consensus round %s not found", roundID),
		}
	}

	// Check expiry.
	if expiresStr, _ := roundEvent["expires_at"].(string); expiresStr != "" {
		if expires, err := time.Parse(time.RFC3339, expiresStr); err == nil {
			if time.Now().After(expires) {
				return &consensus.QuorumError{
					Code: "CQ-002", Class: "round_expired",
					Message: fmt.Sprintf("consensus round %s has expired", roundID),
				}
			}
		}
	}

	// Determine agent ID from env or registry.
	agentID := os.Getenv("CASTEX_AGENT_ID")
	if agentID == "" {
		agentID = "unknown"
	}

	// Load agent secret from registry for HMAC signing.
	secret := ""
	if reg, err := loadRegistry(); err == nil {
		if agentRec, ok := reg.LookupAgent(agentID); ok && agentRec.Secret != nil {
			secret = *agentRec.Secret
		}
	}

	artifactHash, _ := roundEvent["artifact_hash"].(string)
	now := time.Now().UTC()
	sig := ""
	if secret != "" {
		sig = consensus.SignVote(secret, roundID, agentID, vote, artifactHash, now.UnixMilli())
	}

	_ = consensus.NewJSONLEventBus("").Append(ctx, "consensus_vote_cast", map[string]any{
		"round_id":          roundID,
		"agent_id":          agentID,
		"vote":              vote,
		"confidence":        confidence,
		"rationale":         rationale,
		"hmac":              sig,
		"artifact_hash":     artifactHash,
		"timestamp":         now.Format(time.RFC3339),
		"timestamp_unix_ms": now.UnixMilli(),
	})

	fmt.Fprintf(out, "vote cast: %s (round %s, agent %s)\n", vote, roundID, agentID)
	return nil
}

// ─── status ───────────────────────────────────────────────────────────────────

func newConsensusStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <round_id>",
		Short: "Show vote tallies and quorum result for a consensus round",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConsensusStatus(cmd.Context(), cmd.OutOrStdout(), args[0])
		},
	}
	return cmd
}

func runConsensusStatus(_ context.Context, out io.Writer, roundID string) error {
	since := time.Now().AddDate(-1, 0, 0)

	// Load round event.
	roundEvents, err := consensus.ScanConsensusEvents("", "consensus_round_opened", "", since)
	if err != nil {
		return fmt.Errorf("scan event bus: %w", err)
	}
	var roundEvent map[string]any
	for _, e := range roundEvents {
		if rid, _ := e["round_id"].(string); rid == roundID {
			roundEvent = e
			break
		}
	}
	if roundEvent == nil {
		return &consensus.QuorumError{
			Code: "CQ-001", Class: "round_not_found",
			Message: fmt.Sprintf("consensus round %s not found", roundID),
		}
	}

	// Load vote events.
	voteEvents, err := consensus.ScanConsensusEvents("", "consensus_vote_cast", "", since)
	if err != nil {
		return fmt.Errorf("scan vote events: %w", err)
	}

	var votes []consensus.ConsensusVote
	for _, e := range voteEvents {
		if rid, _ := e["round_id"].(string); rid != roundID {
			continue
		}
		agentID, _ := e["agent_id"].(string)
		vote, _ := e["vote"].(string)
		conf, _ := e["confidence"].(float64)
		sig, _ := e["hmac"].(string)
		votes = append(votes, consensus.ConsensusVote{
			RoundID:    roundID,
			AgentID:    agentID,
			Vote:       vote,
			Confidence: conf,
			HMAC:       sig,
		})
	}

	// Build round for quorum computation.
	agents := toStringSlice(roundEvent["required_agents"])
	threshold := 0.51
	if t, ok := roundEvent["quorum_threshold"].(float64); ok {
		threshold = t
	}
	round := consensus.ConsensusRound{
		RoundID:         roundID,
		RequiredAgents:  agents,
		QuorumThreshold: threshold,
	}
	qs := consensus.NewQuorumState(round, votes)
	approve, reject, abstain, required, result := qs.Compute()

	expiresStr, _ := roundEvent["expires_at"].(string)
	fmt.Fprintf(out, "Round:    %s\n", roundID)
	fmt.Fprintf(out, "Expires:  %s\n", expiresStr)
	fmt.Fprintf(out, "Approve:  %d\n", approve)
	fmt.Fprintf(out, "Reject:   %d\n", reject)
	fmt.Fprintf(out, "Abstain:  %d\n", abstain)
	fmt.Fprintf(out, "Required: %d of %d\n", required, len(agents))
	fmt.Fprintf(out, "Result:   %s\n", result)
	return nil
}

// ─── audit ────────────────────────────────────────────────────────────────────

func newConsensusAuditCmd() *cobra.Command {
	var (
		since   string
		verbose bool
		asJSON  bool
	)

	cmd := &cobra.Command{
		Use:   "audit <artifact_path>",
		Short: "Walk consensus events for an artifact and verify hash chain + HMACs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConsensusAudit(cmd.Context(), cmd.OutOrStdout(), args[0], since, verbose, asJSON)
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "Start date for event bus scan (YYYY-MM-DD, default: 1 year ago)")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "Show per-event verification details")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func runConsensusAudit(_ context.Context, out io.Writer, artifactPath, since string, verbose, asJSON bool) error {
	sinceTime := time.Now().AddDate(-1, 0, 0)
	if since != "" {
		t, err := time.Parse("2006-01-02", since)
		if err != nil {
			return fmt.Errorf("invalid --since date %q: %w", since, err)
		}
		sinceTime = t
	}

	// Build secret lookup from registry.
	var secretLookup consensus.AgentSecretLookup
	if reg, err := loadRegistry(); err == nil {
		secretLookup = func(agentID string) (string, bool) {
			a, ok := reg.LookupAgent(agentID)
			if !ok || a.Secret == nil {
				return "", false
			}
			return *a.Secret, true
		}
	}

	result, err := consensus.RunAudit(consensus.AuditConfig{
		ArtifactPath: artifactPath,
		Since:        sinceTime,
		Verbose:      verbose,
		SecretLookup: secretLookup,
	})
	if err != nil {
		return err
	}

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
	} else {
		fmt.Fprint(out, consensus.FormatAuditResult(result, verbose))
	}

	if result.Overall == "fail" {
		// Emit audit_failed event.
		_ = consensus.NewJSONLEventBus("").Append(context.Background(), "consensus_audit_failed", map[string]any{
			"artifact_path":  artifactPath,
			"failure_count":  len(result.Failures),
			"rounds_audited": result.RoundsAudited,
		})
		return fmt.Errorf("audit FAILED: %d failure(s) found", len(result.Failures))
	}
	return nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// loadRegistry loads org.yaml from the standard path.
func loadRegistry() (*registry.Registry, error) {
	path := os.Getenv("ORG_REGISTRY")
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, "code", "personal", "docs", "org.yaml")
	}
	return registry.Load(path)
}

func resolveAgentCWD(a *registry.AgentRecord) string {
	if a.CWD == nil {
		return ""
	}
	cwd := *a.CWD
	if strings.HasPrefix(cwd, "~/") {
		home, _ := os.UserHomeDir()
		cwd = filepath.Join(home, cwd[2:])
	}
	return cwd
}

func inferArtifactType(path string) string {
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.Contains(base, "fdd"):
		return "fdd"
	case strings.Contains(base, "tdd"):
		return "tdd"
	case strings.Contains(base, "epic"):
		return "epic"
	case strings.Contains(base, "directive"):
		return "directive"
	default:
		return "artifact"
	}
}

func toStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	// JSON unmarshals arrays as []interface{}.
	arr, ok := v.([]interface{})
	if !ok {
		// Try raw JSON string.
		if s, ok := v.(string); ok {
			var ss []string
			if err := json.Unmarshal([]byte(s), &ss); err == nil {
				return ss
			}
		}
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
