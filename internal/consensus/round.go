package consensus

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// ConsensusRound represents an open or completed L2 agent quorum round.
type ConsensusRound struct {
	RoundID          string    `json:"round_id"`
	ArtifactPath     string    `json:"artifact_path"`
	ArtifactHash     string    `json:"artifact_hash"`
	ArtifactType     string    `json:"artifact_type"`
	ConsensusType    string    `json:"consensus_type"`
	RequiredAgents   []string  `json:"required_agents"`
	QuorumThreshold  float64   `json:"quorum_threshold"`
	ModelConsensusID string    `json:"model_consensus_id"`
	Status           string    `json:"status"`
	OpenedAt         time.Time `json:"opened_at"`
	ExpiresAt        time.Time `json:"expires_at"`
	PrevHash         string    `json:"prev_hash"`
}

// ConsensusVote is a signed vote cast by an agent for a ConsensusRound.
type ConsensusVote struct {
	RoundID          string    `json:"round_id"`
	AgentID          string    `json:"agent_id"`
	Model            string    `json:"model"`
	ModelConsensusID string    `json:"model_consensus_id"`
	Vote             string    `json:"vote"` // "approve" | "reject" | "abstain"
	Confidence       float64   `json:"confidence"`
	Rationale        string    `json:"rationale"`
	HMAC             string    `json:"hmac"`
	Timestamp        time.Time `json:"timestamp"`
	PrevHash         string    `json:"prev_hash"`
}

// ConsensusGateResult is the final outcome of a completed ConsensusRound.
type ConsensusGateResult struct {
	RoundID         string    `json:"round_id"`
	ArtifactPath    string    `json:"artifact_path"`
	ArtifactHash    string    `json:"artifact_hash"`
	VotesApprove    int       `json:"votes_approve"`
	VotesReject     int       `json:"votes_reject"`
	VotesAbstain    int       `json:"votes_abstain"`
	QuorumRequired  int       `json:"quorum_required"`
	Result          string    `json:"result"` // "approved" | "rejected" | "insufficient" | "human_required"
	NextState       string    `json:"next_state"`
	AttestationHash string    `json:"attestation_hash"`
	Timestamp       time.Time `json:"timestamp"`
	PrevHash        string    `json:"prev_hash"`
}

// Round status values.
const (
	RoundStatusOpen          = "open"
	RoundStatusApproved      = "approved"
	RoundStatusRejected      = "rejected"
	RoundStatusExpired       = "expired"
	RoundStatusHumanRequired = "human_required"
)

// Gate result values.
const (
	GateApproved      = "approved"
	GateRejected      = "rejected"
	GateInsufficient  = "insufficient"
	GateHumanRequired = "human_required"
)

// QuorumRequired returns the minimum approve votes needed to reach quorum.
// It uses floor(N*threshold)+1 but caps at N.
func QuorumRequired(numAgents int, threshold float64) int {
	if numAgents <= 0 {
		return 1
	}
	q := int(float64(numAgents)*threshold) + 1
	if q > numAgents {
		q = numAgents
	}
	return q
}

// HashArtifact returns the sha256 hex digest of the named file's content.
func HashArtifact(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}

// AttestationHash computes sha256(roundID + result + artifactHash + sortedVoteHMACs).
func AttestationHash(roundID, result, artifactHash string, voteHMACs []string) string {
	parts := []string{roundID, result, artifactHash}
	parts = append(parts, voteHMACs...)
	h := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(h[:])
}

// QuorumError is a structured error for quorum protocol violations.
type QuorumError struct {
	Code    string
	Class   string
	Message string
}

func (e *QuorumError) Error() string {
	return fmt.Sprintf("[%s] %s: %s", e.Code, e.Class, e.Message)
}
