package consensus

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WriteVoteDispatch writes a consensus vote request dispatch file to agentCWD/.claude-dispatch/.
// Returns the path of the created dispatch file.
func WriteVoteDispatch(agentCWD, agentID, roundID, artifactPath, artifactHash, artifactType, consensusType string, l1 *ModelConsensusRound, expires time.Time) (string, error) {
	dir := filepath.Join(agentCWD, ".claude-dispatch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir dispatch dir %s: %w", dir, err)
	}

	task := fmt.Sprintf("consensus-vote-%s", roundID)
	dispatchedAt := time.Now().UTC().Format("20060102T150405Z")

	var l1Section string
	if l1 != nil {
		l1Section = fmt.Sprintf(`## L1 Model Consensus Result

Agreement Score: %.4f
Resolution: %s
Divergence Flags: %s

`, l1.AgreementScore, l1.Resolution, strings.Join(l1.DivergenceFlags, ", "))
	}

	content := fmt.Sprintf(
		`---
schema_version: 1
task: %s
agent: %s
dispatched_at: %s
status: pending
claimed_at: null
completed_at: null
---

# Consensus Vote Request: %s

Artifact: %s (hash: %s)
Artifact Type: %s
Consensus Type: %s

%s## Your Task

1. Read the artifact at %s
2. Review the L1 consensus result above
3. Cast your vote: `+"`castex consensus vote --round %s --vote approve|reject|abstain`"+`

## Response

(vote output appended here before dispatch-complete)
`,
		task, agentID, dispatchedAt,
		roundID,
		artifactPath, artifactHash, artifactType, consensusType,
		l1Section,
		artifactPath, roundID,
	)

	path := filepath.Join(dir, task+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write dispatch file %s: %w", path, err)
	}
	return path, nil
}
