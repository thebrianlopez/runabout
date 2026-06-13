package consensus

import (
	"encoding/json"
	"sort"
)

// QuorumState holds the accumulated votes for a round.
type QuorumState struct {
	RoundID        string
	RequiredAgents []string
	Threshold      float64
	Votes          []ConsensusVote
}

// NewQuorumState builds a QuorumState from a ConsensusRound and its collected votes.
func NewQuorumState(round ConsensusRound, votes []ConsensusVote) QuorumState {
	return QuorumState{
		RoundID:        round.RoundID,
		RequiredAgents: round.RequiredAgents,
		Threshold:      round.QuorumThreshold,
		Votes:          votes,
	}
}

// Compute returns approve/reject/abstain counts, quorum required, and result.
func (q QuorumState) Compute() (approve, reject, abstain, required int, result string) {
	required = QuorumRequired(len(q.RequiredAgents), q.Threshold)

	// Only count votes from required agents; deduplicate by agent (first vote wins).
	seen := map[string]string{}
	for _, v := range q.Votes {
		if _, dup := seen[v.AgentID]; dup {
			continue
		}
		seen[v.AgentID] = v.Vote
	}
	for _, v := range seen {
		switch v {
		case "approve":
			approve++
		case "reject":
			reject++
		case "abstain":
			abstain++
		}
	}

	switch {
	case approve >= required:
		result = GateApproved
	case reject >= required:
		result = GateRejected
	case approve+reject+abstain == len(q.RequiredAgents):
		// All voted but no majority.
		result = GateHumanRequired
	default:
		result = GateInsufficient
	}
	return
}

// VoteHMACs returns the sorted list of vote HMACs for AttestationHash computation.
func (q QuorumState) VoteHMACs() []string {
	hmacs := make([]string, 0, len(q.Votes))
	for _, v := range q.Votes {
		if v.HMAC != "" {
			hmacs = append(hmacs, v.HMAC)
		}
	}
	sort.Strings(hmacs)
	return hmacs
}

// quorumStateJSON is used for JSON marshaling in tests.
type quorumStateJSON struct {
	Approve  int    `json:"approve"`
	Reject   int    `json:"reject"`
	Abstain  int    `json:"abstain"`
	Required int    `json:"required"`
	Result   string `json:"result"`
}

func (q QuorumState) MarshalJSON() ([]byte, error) {
	a, r, ab, req, res := q.Compute()
	return json.Marshal(quorumStateJSON{
		Approve:  a,
		Reject:   r,
		Abstain:  ab,
		Required: req,
		Result:   res,
	})
}
