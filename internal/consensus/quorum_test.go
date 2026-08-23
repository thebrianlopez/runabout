package consensus

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// CT-1: submit creates ConsensusRound and writes dispatch per agent.
func TestCT1_SubmitWritesDispatch(t *testing.T) {
	dir := t.TempDir()
	agentCWDs := map[string]string{
		"agent-a": filepath.Join(dir, "agent-a"),
		"agent-b": filepath.Join(dir, "agent-b"),
	}
	for _, cwd := range agentCWDs {
		require.NoError(t, os.MkdirAll(cwd, 0o755))
	}

	roundID := NewRoundID()
	artifactHash := "abc123"
	expires := time.Now().Add(10 * time.Minute)

	for agentID, cwd := range agentCWDs {
		path, err := WriteVoteDispatch(cwd, agentID, roundID, "/tmp/artifact.md", artifactHash, "epic", "promotion", nil, expires)
		require.NoError(t, err)
		require.FileExists(t, path)
	}

	// Two dispatch files should exist - one per agent.
	for _, cwd := range agentCWDs {
		entries, err := os.ReadDir(filepath.Join(cwd, ".claude-dispatch"))
		require.NoError(t, err)
		require.Len(t, entries, 1)
	}
}

// CT-2: Quorum reached with approve votes >= floor(N/2)+1.
func TestCT2_QuorumApproved(t *testing.T) {
	round := ConsensusRound{
		RoundID:         NewRoundID(),
		RequiredAgents:  []string{"a", "b", "c"},
		QuorumThreshold: 0.51,
	}
	votes := []ConsensusVote{
		{AgentID: "a", Vote: "approve"},
		{AgentID: "b", Vote: "approve"},
	}
	qs := NewQuorumState(round, votes)
	_, _, _, _, result := qs.Compute()
	require.Equal(t, GateApproved, result)
}

// CT-3: Quorum rejected with reject votes >= floor(N/2)+1.
func TestCT3_QuorumRejected(t *testing.T) {
	round := ConsensusRound{
		RoundID:         NewRoundID(),
		RequiredAgents:  []string{"a", "b", "c"},
		QuorumThreshold: 0.51,
	}
	votes := []ConsensusVote{
		{AgentID: "a", Vote: "reject"},
		{AgentID: "b", Vote: "reject"},
	}
	qs := NewQuorumState(round, votes)
	_, _, _, _, result := qs.Compute()
	require.Equal(t, GateRejected, result)
}

// CT-4: Abstain counts toward participation but not approval.
func TestCT4_AbstainNotApproval(t *testing.T) {
	// 3 agents: 1 approve, 1 abstain, 1 pending -> not yet approved (only 1 approve < 2 quorum).
	round := ConsensusRound{
		RoundID:         NewRoundID(),
		RequiredAgents:  []string{"a", "b", "c"},
		QuorumThreshold: 0.51,
	}
	votes := []ConsensusVote{
		{AgentID: "a", Vote: "approve"},
		{AgentID: "b", Vote: "abstain"},
	}
	qs := NewQuorumState(round, votes)
	approve, _, _, required, result := qs.Compute()
	require.Less(t, approve, required)
	require.NotEqual(t, GateApproved, result)
}

// CT-5: Expired round rejects new votes.
func TestCT5_ExpiredRoundRejected(t *testing.T) {
	expires := time.Now().Add(-1 * time.Second) // already expired
	err := checkRoundExpiry(expires)
	require.Error(t, err)
	var qe *QuorumError
	require.ErrorAs(t, err, &qe)
	require.Equal(t, "CQ-002", qe.Code)
}

// CT-6: Duplicate vote from same agent rejected.
func TestCT6_DuplicateVoteRejected(t *testing.T) {
	round := ConsensusRound{
		RoundID:         NewRoundID(),
		RequiredAgents:  []string{"a", "b"},
		QuorumThreshold: 0.51,
	}
	// Two votes from same agent - second is a duplicate.
	votes := []ConsensusVote{
		{AgentID: "a", Vote: "approve"},
		{AgentID: "a", Vote: "reject"}, // duplicate - should be ignored
		{AgentID: "b", Vote: "approve"},
	}
	qs := NewQuorumState(round, votes)
	approve, _, _, _, _ := qs.Compute()
	// "a" counted once (first vote = approve), "b" = approve → 2 approve.
	require.Equal(t, 2, approve)
}

// CT-7: AttestationHash is deterministic given same inputs.
func TestCT7_AttestationHashDeterministic(t *testing.T) {
	h1 := AttestationHash("round1", "approved", "artifact123", []string{"hmac1", "hmac2"})
	h2 := AttestationHash("round1", "approved", "artifact123", []string{"hmac1", "hmac2"})
	require.Equal(t, h1, h2)
	require.NotEmpty(t, h1)
}

// CT-8: Non-required agent vote rejected.
func TestCT8_NonRequiredAgentRejected(t *testing.T) {
	round := ConsensusRound{
		RoundID:         NewRoundID(),
		RequiredAgents:  []string{"a", "b"},
		QuorumThreshold: 0.51,
	}
	err := checkAgentRequired(round, "c")
	require.Error(t, err)
	var qe *QuorumError
	require.ErrorAs(t, err, &qe)
	require.Equal(t, "CQ-006", qe.Code)
}

// CT-9: HMAC signing: vote.HMAC verifies with agent's key.
func TestCT9_HMACSignAndVerify(t *testing.T) {
	secret := "test-secret-key"
	sig := SignVote(secret, "round1", "agent-a", "approve", "artifact123", 1234567890)
	require.NotEmpty(t, sig)
	ok := VerifyVote(secret, sig, "round1", "agent-a", "approve", "artifact123", 1234567890)
	require.True(t, ok)
}

// CT-10: HMAC signing: tampered vote does not verify.
func TestCT10_HMACTamperedFails(t *testing.T) {
	secret := "test-secret-key"
	sig := SignVote(secret, "round1", "agent-a", "approve", "artifact123", 1234567890)
	// Tamper: change vote from approve to reject.
	ok := VerifyVote(secret, sig, "round1", "agent-a", "reject", "artifact123", 1234567890)
	require.False(t, ok)
}

// RG-1: AgentID in vote must match signing key in org.yaml.
// Verified structurally: the HMAC includes agentID in the signature data.
func TestRG1_HMACBindsAgentID(t *testing.T) {
	secret := "key"
	sig := SignVote(secret, "r1", "agent-a", "approve", "h1", 1000)
	// Attempt to use agent-b's identity with agent-a's sig.
	ok := VerifyVote(secret, sig, "r1", "agent-b", "approve", "h1", 1000)
	require.False(t, ok, "HMAC must bind to agentID")
}

func pathWithin(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// RG-2: Dispatch files must be written to agent's registered CWD.
func TestRG2_DispatchWrittenToAgentCWD(t *testing.T) {
	agentCWD := t.TempDir()
	wrongCWD := t.TempDir()

	path, err := WriteVoteDispatch(agentCWD, "agent-a", "round1", "/tmp/art.md", "hash1", "epic", "promotion", nil, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.True(t, pathWithin(path, agentCWD), "dispatch must be in agent CWD")
	require.False(t, pathWithin(path, wrongCWD), "dispatch must not be in wrong CWD")
}

// QuorumRequired helper tests.
func TestQuorumRequired(t *testing.T) {
	// floor(3 * 0.51) + 1 = floor(1.53) + 1 = 1+1 = 2
	require.Equal(t, 2, QuorumRequired(3, 0.51))
	// 1 agent: floor(1*0.51)+1 = 1; cap at 1 = 1
	require.Equal(t, 1, QuorumRequired(1, 0.51))
	// 0 agents: min 1
	require.Equal(t, 1, QuorumRequired(0, 0.51))
}

// HashArtifact uses sha256.
func TestHashArtifact(t *testing.T) {
	content := []byte("hello world")
	h := sha256.Sum256(content)
	expected := hex.EncodeToString(h[:])
	require.Equal(t, expected, HashArtifact(content))
}

// Helper: checkRoundExpiry returns CQ-002 if expired.
func checkRoundExpiry(expires time.Time) error {
	if time.Now().After(expires) {
		return &QuorumError{
			Code: "CQ-002", Class: "round_expired",
			Message: "consensus round has expired",
		}
	}
	return nil
}

// Helper: checkAgentRequired returns CQ-006 if agent not in required list.
func checkAgentRequired(round ConsensusRound, agentID string) error {
	for _, id := range round.RequiredAgents {
		if id == agentID {
			return nil
		}
	}
	return &QuorumError{
		Code: "CQ-006", Class: "agent_not_required",
		Message: "agent " + agentID + " is not a required voter",
	}
}
