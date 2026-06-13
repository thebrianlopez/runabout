package consensus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// AuditResult is the structured output of a consensus audit run.
type AuditResult struct {
	ArtifactPath      string
	RoundsAudited     int
	VotesAudited      int
	HashChainVerified bool
	HMACsVerified     bool
	QuorumVerified    bool
	Failures          []AuditFailure
	Overall           string // "pass" | "fail"
}

// AuditFailure describes a single integrity violation found during audit.
type AuditFailure struct {
	EventIndex  int
	EventType   string
	FailureType string // "hash_chain_broken" | "hmac_mismatch" | "quorum_math_mismatch" | "attestation_hash_mismatch"
	Expected    string
	Actual      string
	AgentID     string
}

// AgentSecretLookup returns the HMAC secret for the given agent ID.
// Returns ("", false) if the agent is unknown.
type AgentSecretLookup func(agentID string) (secret string, ok bool)

// AuditConfig controls the scope of an audit.
type AuditConfig struct {
	ArtifactPath string
	Since        time.Time
	Verbose      bool
	EventBusDir  string
	SecretLookup AgentSecretLookup
}

// RunAudit performs a full audit of all consensus events for the given artifact.
func RunAudit(cfg AuditConfig) (*AuditResult, error) {
	since := cfg.Since
	if since.IsZero() {
		since = time.Now().AddDate(-1, 0, 0)
	}

	result := &AuditResult{
		ArtifactPath:      cfg.ArtifactPath,
		HashChainVerified: true,
		HMACsVerified:     true,
		QuorumVerified:    true,
		Overall:           "pass",
	}

	// Collect all consensus events for this artifact across all event types.
	allEvents, err := collectArtifactEvents(cfg.EventBusDir, cfg.ArtifactPath, since)
	if err != nil {
		return nil, fmt.Errorf("collect events: %w", err)
	}

	if len(allEvents) == 0 {
		return nil, &QuorumError{
			Code:    "AU-006",
			Class:   "no_consensus_events",
			Message: fmt.Sprintf("no consensus events found for %s", cfg.ArtifactPath),
		}
	}

	// Group events by round ID.
	roundEvents := groupByRound(allEvents)
	result.RoundsAudited = len(roundEvents)

	for roundID, events := range roundEvents {
		failures := auditRound(roundID, events, cfg.SecretLookup, result)
		result.Failures = append(result.Failures, failures...)
	}

	if len(result.Failures) > 0 {
		result.Overall = "fail"
		result.HashChainVerified = !hasFailureType(result.Failures, "hash_chain_broken")
		result.HMACsVerified = !hasFailureType(result.Failures, "hmac_mismatch")
		result.QuorumVerified = !hasFailureType(result.Failures, "quorum_math_mismatch")
	}

	return result, nil
}

func collectArtifactEvents(dir, artifactPath string, since time.Time) ([]map[string]any, error) {
	consensusEventTypes := []string{
		"consensus_round_opened",
		"consensus_vote_cast",
		"consensus_gate_result",
	}
	var all []map[string]any
	for _, et := range consensusEventTypes {
		events, err := ScanConsensusEvents(dir, et, artifactPath, since)
		if err != nil {
			return nil, err
		}
		all = append(all, events...)
	}
	return all, nil
}

func groupByRound(events []map[string]any) map[string][]map[string]any {
	groups := map[string][]map[string]any{}
	for _, e := range events {
		rid, _ := e["round_id"].(string)
		if rid == "" {
			continue
		}
		groups[rid] = append(groups[rid], e)
	}
	return groups
}

// auditRound checks hash chain continuity, HMAC validity, and quorum math for one round.
func auditRound(roundID string, events []map[string]any, lookup AgentSecretLookup, result *AuditResult) []AuditFailure {
	// Sort events by timestamp to walk in order.
	sort.Slice(events, func(i, j int) bool {
		ti, _ := events[i]["timestamp"].(string)
		tj, _ := events[j]["timestamp"].(string)
		return ti < tj
	})

	var failures []AuditFailure
	var prevHash string // empty = no M1 hash chain yet (pre-M1 events are not chain-verified)

	// Track votes for quorum re-computation.
	var votes []ConsensusVote
	var roundEvent map[string]any
	var gateResult map[string]any

	for idx, e := range events {
		et, _ := e["event_type"].(string)

		// Hash chain verification: if prev_hash is present, verify it.
		if ph, ok := e["prev_hash"].(string); ok && ph != "" && prevHash != "" {
			if ph != prevHash {
				failures = append(failures, AuditFailure{
					EventIndex:  idx,
					EventType:   et,
					FailureType: "hash_chain_broken",
					Expected:    prevHash,
					Actual:      ph,
				})
			}
		}
		// Compute this event's hash for the next event to verify.
		if raw, err := json.Marshal(e); err == nil {
			h := sha256.Sum256(raw)
			prevHash = hex.EncodeToString(h[:])
		}

		switch et {
		case "consensus_round_opened":
			roundEvent = e

		case "consensus_vote_cast":
			result.VotesAudited++
			agentID, _ := e["agent_id"].(string)
			vote, _ := e["vote"].(string)
			sig, _ := e["hmac"].(string)
			artifactHash, _ := e["artifact_hash"].(string)
			// Prefer timestamp_unix_ms (exact) over parsing timestamp string (second precision).
			var tsMS int64
			if ms, ok := e["timestamp_unix_ms"].(float64); ok {
				tsMS = int64(ms)
			} else if tsStr, _ := e["timestamp"].(string); tsStr != "" {
				if t, err := time.Parse(time.RFC3339, tsStr); err == nil {
					tsMS = t.UnixMilli()
				}
			}

			// HMAC verification.
			if sig != "" && lookup != nil {
				if secret, ok := lookup(agentID); ok && secret != "" {
					if !VerifyVote(secret, sig, roundID, agentID, vote, artifactHash, tsMS) {
						failures = append(failures, AuditFailure{
							EventIndex:  idx,
							EventType:   et,
							FailureType: "hmac_mismatch",
							AgentID:     agentID,
							Expected:    "valid HMAC",
							Actual:      "HMAC mismatch",
						})
					}
				} else if !ok {
					// Agent secret missing - warning, not failure.
					fmt.Printf("warning [AU-005]: cannot verify HMAC for agent %s: secret not in registry\n", agentID)
				}
			}

			var voteTS time.Time
			if tsStr, _ := e["timestamp"].(string); tsStr != "" {
				voteTS, _ = time.Parse(time.RFC3339, tsStr)
			}
			votes = append(votes, ConsensusVote{
				RoundID:   roundID,
				AgentID:   agentID,
				Vote:      vote,
				HMAC:      sig,
				Timestamp: voteTS,
			})

		case "consensus_gate_result":
			gateResult = e
		}
	}

	// Quorum re-computation: compare stored gate result to re-computed.
	if gateResult != nil && roundEvent != nil {
		agents := toStringSliceFromAny(roundEvent["required_agents"])
		threshold := 0.51
		if t, ok := roundEvent["quorum_threshold"].(float64); ok {
			threshold = t
		}
		round := ConsensusRound{
			RoundID:         roundID,
			RequiredAgents:  agents,
			QuorumThreshold: threshold,
		}
		qs := NewQuorumState(round, votes)
		_, _, _, _, recomputedResult := qs.Compute()

		storedResult, _ := gateResult["result"].(string)
		if storedResult != "" && storedResult != recomputedResult {
			failures = append(failures, AuditFailure{
				EventIndex:  len(events) - 1,
				EventType:   "consensus_gate_result",
				FailureType: "quorum_math_mismatch",
				Expected:    recomputedResult,
				Actual:      storedResult,
			})
		}

		// AttestationHash re-computation.
		storedAttestation, _ := gateResult["attestation_hash"].(string)
		if storedAttestation != "" {
			artifactHash, _ := roundEvent["artifact_hash"].(string)
			recomputed := AttestationHash(roundID, storedResult, artifactHash, qs.VoteHMACs())
			if recomputed != storedAttestation {
				failures = append(failures, AuditFailure{
					EventIndex:  len(events) - 1,
					EventType:   "consensus_gate_result",
					FailureType: "attestation_hash_mismatch",
					Expected:    storedAttestation,
					Actual:      recomputed,
				})
			}
		}
	}

	return failures
}

func hasFailureType(failures []AuditFailure, ft string) bool {
	for _, f := range failures {
		if f.FailureType == ft {
			return true
		}
	}
	return false
}

// FormatAuditResult renders a human-readable audit report.
func FormatAuditResult(r *AuditResult, verbose bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Artifact:  %s\n", r.ArtifactPath)
	fmt.Fprintf(&sb, "Rounds:    %d\n", r.RoundsAudited)
	fmt.Fprintf(&sb, "Votes:     %d\n", r.VotesAudited)
	fmt.Fprintf(&sb, "HashChain: %s\n", boolVerdict(r.HashChainVerified))
	fmt.Fprintf(&sb, "HMACs:     %s\n", boolVerdict(r.HMACsVerified))
	fmt.Fprintf(&sb, "Quorum:    %s\n", boolVerdict(r.QuorumVerified))
	fmt.Fprintf(&sb, "Overall:   %s\n", r.Overall)
	if verbose && len(r.Failures) > 0 {
		fmt.Fprintf(&sb, "\nFailures:\n")
		for _, f := range r.Failures {
			fmt.Fprintf(&sb, "  [%d] %s: %s (expected=%s, actual=%s)\n",
				f.EventIndex, f.EventType, f.FailureType, f.Expected, f.Actual)
		}
	}
	return sb.String()
}

func boolVerdict(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func toStringSliceFromAny(v any) []string {
	if v == nil {
		return nil
	}
	arr, ok := v.([]interface{})
	if !ok {
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
