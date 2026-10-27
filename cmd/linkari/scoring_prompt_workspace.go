package main

// Workspace routing prompt construction. Label-only MVP.
//
// buildWorkspaceContextBlock returns a prompt block that instructs the model
// to assign at most one workspace from the registry. Returns "" when the
// registry is nil or empty - the prompt is byte-for-byte unchanged (same
// backward-compat contract as buildScoringPrompt).

import (
	"fmt"
	"strings"
)

// buildWorkspaceContextBlock builds the workspace assignment section.
//
// Stage 1 (free): when the user's share-time tags resolve to exactly one
// workspace, only that candidate is presented with high confidence. Stage 2:
// the full registry (bounded by maxTokens) is presented for model inference.
//
// The block is self-contained: it carries its own output instruction so the
// rubric template does not change.
func buildWorkspaceContextBlock(reg *WorkspaceRegistry, userTags []string, maxTokens int) string {
	if reg == nil || len(reg.Workspaces) == 0 {
		return ""
	}
	var b strings.Builder

	if name, ok := reg.MatchTags(userTags); ok {
		for _, ws := range reg.Workspaces {
			if ws.Name == name {
				fmt.Fprintf(&b, "\n\n## Workspace Assignment\n\n"+
					"The user's share-time tags identify the target workspace. Emit "+
					"\"workspace\": %q with \"workspace_confidence\": 1.0.\n\n", ws.Name)
				if ws.Scope != "" {
					fmt.Fprintf(&b, "Workspace context: %s\n", ws.Scope)
				}
				return b.String()
			}
		}
	}

	b.WriteString("\n\n## Workspace Assignment\n\n" +
		"Assign AT MOST ONE workspace for this item from the registry below. " +
		"Emit \"workspace\": \"<name|none>\" and \"workspace_confidence\": <0.0-1.0> " +
		"in your verdict. Prefer \"none\" when uncertain - a wrong assignment is " +
		"worse than no assignment.\n\n<WORKSPACE_REGISTRY>\n")
	remaining := maxTokens
	if remaining <= 0 {
		remaining = 400
	}
	for _, ws := range reg.Workspaces {
		line := fmt.Sprintf("- %s: %s\n", ws.Name, ws.Scope)
		if approxTokens(b.String()+line) > remaining {
			break
		}
		b.WriteString(line)
	}
	b.WriteString("</WORKSPACE_REGISTRY>\n")
	return b.String()
}

// normalizeWorkspaceAssignment validates the model-emitted workspace value
// against the registry. Unknown names collapse to "" (unassigned) so a
// hallucinated workspace never persists. "none" normalizes to "".
func normalizeWorkspaceAssignment(reg *WorkspaceRegistry, workspace string) string {
	ws := strings.ToLower(strings.TrimSpace(workspace))
	if ws == "" || ws == "none" {
		return ""
	}
	if reg != nil && reg.HasWorkspace(ws) {
		return ws
	}
	return ""
}

// clampWorkspaceConfidence bounds the model-emitted confidence to [0, 1].
// Non-finite and out-of-range values collapse to 0 - the report treats 0 as
// "no usable confidence signal" rather than maximum confidence.
func clampWorkspaceConfidence(c float64) float64 {
	if c < 0 || c > 1 {
		return 0
	}
	return c
}
