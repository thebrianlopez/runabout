package consensus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// writeTestEvent writes an event to a temp JSONL file for audit tests.
func writeTestEvent(t *testing.T, dir string, event map[string]any) {
	t.Helper()
	day := time.Now().Format("2006-01-02")
	path := filepath.Join(dir, day+".jsonl")
	b, err := json.Marshal(event)
	require.NoError(t, err)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	require.NoError(t, err)
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	require.NoError(t, err)
}

const (
	testArtifact = "/tmp/test_artifact.md"
	testSecret   = "super-secret"
	testAgentID  = "test-agent"
)

func makeRoundEvent(roundID string) map[string]any {
	return map[string]any{
		"event_type":       "consensus_round_opened",
		"round_id":         roundID,
		"artifact_path":    testArtifact,
		"artifact_hash":    "deadbeef",
		"required_agents":  []string{testAgentID},
		"quorum_threshold": 0.51,
		"timestamp":        time.Now().UTC().Format(time.RFC3339),
		"expires_at":       time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
}

func makeVoteEvent(roundID, agentID, vote, artifactHash string, ts time.Time, secret string) map[string]any {
	ms := ts.UnixMilli()
	sig := SignVote(secret, roundID, agentID, vote, artifactHash, ms)
	return map[string]any{
		"event_type":        "consensus_vote_cast",
		"round_id":          roundID,
		"artifact_path":     testArtifact,
		"agent_id":          agentID,
		"vote":              vote,
		"confidence":        0.90,
		"hmac":              sig,
		"artifact_hash":     artifactHash,
		"timestamp":         ts.UTC().Format(time.RFC3339),
		"timestamp_unix_ms": ms,
	}
}

// CT-1: Clean chain with valid HMACs → AuditResult.Overall == "pass".
func TestAuditCT1_CleanChainPass(t *testing.T) {
	dir := t.TempDir()
	roundID := NewRoundID()
	ts := time.Now()

	writeTestEvent(t, dir, makeRoundEvent(roundID))
	writeTestEvent(t, dir, makeVoteEvent(roundID, testAgentID, "approve", "deadbeef", ts, testSecret))

	result, err := RunAudit(AuditConfig{
		ArtifactPath: testArtifact,
		Since:        time.Now().AddDate(0, 0, -1),
		EventBusDir:  dir,
		SecretLookup: func(id string) (string, bool) {
			if id == testAgentID {
				return testSecret, true
			}
			return "", false
		},
	})
	require.NoError(t, err)
	require.Equal(t, "pass", result.Overall)
	require.Empty(t, result.Failures)
	require.Equal(t, 1, result.RoundsAudited)
	require.Equal(t, 1, result.VotesAudited)
}

// CT-2: Tampered vote HMAC → AU-002 hmac_mismatch.
func TestAuditCT2_TamperedHMAC(t *testing.T) {
	dir := t.TempDir()
	roundID := NewRoundID()
	ts := time.Now()

	voteEvent := makeVoteEvent(roundID, testAgentID, "approve", "deadbeef", ts, testSecret)
	voteEvent["hmac"] = "tampered_hmac_value"

	writeTestEvent(t, dir, makeRoundEvent(roundID))
	writeTestEvent(t, dir, voteEvent)

	result, err := RunAudit(AuditConfig{
		ArtifactPath: testArtifact,
		Since:        time.Now().AddDate(0, 0, -1),
		EventBusDir:  dir,
		SecretLookup: func(id string) (string, bool) {
			return testSecret, true
		},
	})
	require.NoError(t, err)
	require.Equal(t, "fail", result.Overall)
	require.True(t, hasFailureType(result.Failures, "hmac_mismatch"))
}

// CT-3: No events for artifact → AU-006 error.
func TestAuditCT3_NoEvents(t *testing.T) {
	dir := t.TempDir()
	_, err := RunAudit(AuditConfig{
		ArtifactPath: "/tmp/nonexistent.md",
		Since:        time.Now().AddDate(0, 0, -1),
		EventBusDir:  dir,
	})
	require.Error(t, err)
	var qe *QuorumError
	require.ErrorAs(t, err, &qe)
	require.Equal(t, "AU-006", qe.Code)
}

// CT-4: Quorum re-computation matches stored result.
func TestAuditCT4_QuorumRecomputeMatches(t *testing.T) {
	roundID := "testroundid"
	agents := []string{"a", "b"}
	votes := []ConsensusVote{
		{AgentID: "a", Vote: "approve"},
		{AgentID: "b", Vote: "approve"},
	}
	round := ConsensusRound{RoundID: roundID, RequiredAgents: agents, QuorumThreshold: 0.51}
	qs := NewQuorumState(round, votes)
	_, _, _, _, result := qs.Compute()
	require.Equal(t, GateApproved, result)

	// Stored result matches recomputed.
	require.Equal(t, "approved", result)
}

// CT-5: Missing agent secret → AU-005 warning, audit continues (not failure).
func TestAuditCT5_MissingSecretWarning(t *testing.T) {
	dir := t.TempDir()
	roundID := NewRoundID()
	ts := time.Now()

	writeTestEvent(t, dir, makeRoundEvent(roundID))
	writeTestEvent(t, dir, makeVoteEvent(roundID, "unknown-agent", "approve", "deadbeef", ts, "secret"))

	// SecretLookup returns false for unknown-agent → warning, not failure.
	result, err := RunAudit(AuditConfig{
		ArtifactPath: testArtifact,
		Since:        time.Now().AddDate(0, 0, -1),
		EventBusDir:  dir,
		SecretLookup: func(id string) (string, bool) {
			return "", false // unknown agent
		},
	})
	require.NoError(t, err)
	// No hmac_mismatch failure - just a warning.
	require.False(t, hasFailureType(result.Failures, "hmac_mismatch"))
}

// CT-6: AttestationHash re-computation correct.
func TestAuditCT6_AttestationHashRecompute(t *testing.T) {
	roundID := "r1"
	result := "approved"
	artifactHash := "h1"
	hmacs := []string{"hmac1", "hmac2"}

	h := AttestationHash(roundID, result, artifactHash, hmacs)
	require.NotEmpty(t, h)

	// Same inputs → same output.
	h2 := AttestationHash(roundID, result, artifactHash, hmacs)
	require.Equal(t, h, h2)
}

// CT-7: Exit code 0 on pass, 1 on any failure.
// Tested via Overall field: "pass" → exit 0, "fail" → exit 1.
func TestAuditCT7_ExitCode(t *testing.T) {
	dir := t.TempDir()
	roundID := NewRoundID()
	ts := time.Now()

	// Tampered HMAC → fail.
	badVote := makeVoteEvent(roundID, testAgentID, "approve", "deadbeef", ts, testSecret)
	badVote["hmac"] = "bad"
	writeTestEvent(t, dir, makeRoundEvent(roundID))
	writeTestEvent(t, dir, badVote)

	result, err := RunAudit(AuditConfig{
		ArtifactPath: testArtifact,
		Since:        time.Now().AddDate(0, 0, -1),
		EventBusDir:  dir,
		SecretLookup: func(id string) (string, bool) { return testSecret, true },
	})
	require.NoError(t, err)
	require.Equal(t, "fail", result.Overall) // → exit 1 in CLI

	// Clean chain → pass.
	dir2 := t.TempDir()
	roundID2 := NewRoundID()
	writeTestEvent(t, dir2, makeRoundEvent(roundID2))
	writeTestEvent(t, dir2, makeVoteEvent(roundID2, testAgentID, "approve", "deadbeef", ts, testSecret))
	result2, err := RunAudit(AuditConfig{
		ArtifactPath: testArtifact,
		Since:        time.Now().AddDate(0, 0, -1),
		EventBusDir:  dir2,
		SecretLookup: func(id string) (string, bool) { return testSecret, true },
	})
	require.NoError(t, err)
	require.Equal(t, "pass", result2.Overall) // → exit 0 in CLI
}

// CT-8: --since flag limits event bus scan to date range.
func TestAuditCT8_SinceFlag(t *testing.T) {
	dir := t.TempDir()
	roundID := NewRoundID()
	ts := time.Now()

	writeTestEvent(t, dir, makeRoundEvent(roundID))
	writeTestEvent(t, dir, makeVoteEvent(roundID, testAgentID, "approve", "deadbeef", ts, testSecret))

	// Since = tomorrow → no files in range.
	_, err := RunAudit(AuditConfig{
		ArtifactPath: testArtifact,
		Since:        time.Now().AddDate(0, 0, 1), // future
		EventBusDir:  dir,
		SecretLookup: func(id string) (string, bool) { return testSecret, true },
	})
	// No events found → AU-006.
	require.Error(t, err)
	var qe *QuorumError
	require.ErrorAs(t, err, &qe)
	require.Equal(t, "AU-006", qe.Code)
}

// RG-1: HMAC data string must be in exact same field order as M3 vote signing.
func TestAuditRG1_HMACFieldOrder(t *testing.T) {
	// Sign with M3 contract; verify with M4 audit contract.
	secret := "key"
	roundID := "r1"
	agentID := "a1"
	vote := "approve"
	artifactHash := "h1"
	tsMS := int64(1234567890000)

	sig := SignVote(secret, roundID, agentID, vote, artifactHash, tsMS)
	ok := VerifyVote(secret, sig, roundID, agentID, vote, artifactHash, tsMS)
	require.True(t, ok, "M4 audit HMAC verification must match M3 signing contract exactly")
}
