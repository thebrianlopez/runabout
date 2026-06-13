package consensus

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// SignVote computes HMAC-SHA256 over the canonical vote fields.
// Field concatenation order is fixed: round_id|agent_id|vote|artifact_hash|timestamp_unix_ms
// The secret must not be logged or included in any output.
func SignVote(secret, roundID, agentID, vote, artifactHash string, timestampUnixMS int64) string {
	data := voteSignatureData(roundID, agentID, vote, artifactHash, timestampUnixMS)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyVote returns true if sig matches the expected HMAC for the given fields.
func VerifyVote(secret, sig, roundID, agentID, vote, artifactHash string, timestampUnixMS int64) bool {
	expected := SignVote(secret, roundID, agentID, vote, artifactHash, timestampUnixMS)
	return hmac.Equal([]byte(sig), []byte(expected))
}

// voteSignatureData builds the canonical string to be signed/verified.
// Order: round_id|agent_id|vote|artifact_hash|timestamp_unix_ms
func voteSignatureData(roundID, agentID, vote, artifactHash string, timestampUnixMS int64) string {
	parts := []string{roundID, agentID, vote, artifactHash, strconv.FormatInt(timestampUnixMS, 10)}
	return strings.Join(parts, "|")
}

// HMACError is returned when vote HMAC verification fails.
type HMACError struct {
	AgentID string
	RoundID string
}

func (e *HMACError) Error() string {
	return fmt.Sprintf("[CQ-005] hmac_verification_failed: vote integrity check failed for agent %s in round %s", e.AgentID, e.RoundID)
}
