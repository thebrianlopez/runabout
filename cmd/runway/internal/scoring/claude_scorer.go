package scoring

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	defaultScoringModel   = "claude-haiku-4-5-20251001"
	defaultScoringTimeout = 10 * time.Second
)

// ClaudeLLMScorer implements LLMScorer by calling the claude CLI.
// Authentication uses the existing claude CLI OAuth2 session — no API key required.
// Model is pinned to claude-haiku-4-5 per TDD §12; upgrades require calibration re-run.
type ClaudeLLMScorer struct {
	Model   string        // defaults to claude-haiku-4-5-20251001
	Timeout time.Duration // defaults to 10s; 0 uses default
}

func (c *ClaudeLLMScorer) model() string {
	if c.Model != "" {
		return c.Model
	}
	return defaultScoringModel
}

func (c *ClaudeLLMScorer) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return defaultScoringTimeout
}

// ScoreWithLLM calls the claude CLI with the scoring prompt and parses the response.
// Returns ErrScoreLLMTimeout on deadline exceeded, ErrScoreInvalidResponse on parse failure.
func (c *ClaudeLLMScorer) ScoreWithLLM(ctx context.Context, jd *JobDescription, resume *Resume) (*LLMScoreResponse, error) {
	prompt := buildScoringPrompt(jd, resume)

	callCtx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	cmd := exec.CommandContext(callCtx, "claude", "-p", prompt, "--model", c.model())
	out, err := cmd.Output()
	if err != nil {
		if callCtx.Err() == context.DeadlineExceeded {
			return nil, ErrScoreLLMTimeout
		}
		return nil, ErrScoreInvalidResponse
	}

	resp, err := parseScoringResponse(out)
	if err != nil {
		return nil, ErrScoreInvalidResponse
	}
	return resp, nil
}

// buildScoringPrompt constructs the LLM scoring prompt from structured JD and resume data.
// Security invariants: no API keys, passwords, or email addresses are included.
// Resume content (skills, experience) is included for scoring but never logged by this package.
func buildScoringPrompt(jd *JobDescription, resume *Resume) string {
	var b strings.Builder

	b.WriteString("You are a technical recruiter scoring a candidate resume against a job description.\n")
	b.WriteString("Score the match across four dimensions and return ONLY valid JSON with no other text.\n\n")

	fmt.Fprintf(&b, "## Job Description\n")
	fmt.Fprintf(&b, "Title: %s\n", jd.Title)
	fmt.Fprintf(&b, "Company: %s\n", jd.Company)
	fmt.Fprintf(&b, "Seniority Level: %s\n", jd.SeniorityLevel)
	if len(jd.Domain) > 0 {
		fmt.Fprintf(&b, "Domain: %s\n", strings.Join(jd.Domain, ", "))
	}
	fmt.Fprintf(&b, "Required Skills: %s\n", strings.Join(jd.RequiredSkills, ", "))
	if len(jd.PreferredSkills) > 0 {
		fmt.Fprintf(&b, "Preferred Skills: %s\n", strings.Join(jd.PreferredSkills, ", "))
	}

	b.WriteString("\n## Candidate Resume\n")
	fmt.Fprintf(&b, "Summary: %s\n\n", resume.Summary)

	b.WriteString("Skills:\n")
	for category, skills := range resume.Skills {
		fmt.Fprintf(&b, "  %s: %s\n", category, strings.Join(skills, ", "))
	}

	b.WriteString("\nExperience:\n")
	for _, entry := range resume.Experience {
		fmt.Fprintf(&b, "  %s:\n", entry.Company)
		for _, role := range entry.Roles {
			fmt.Fprintf(&b, "    Role: %s\n", role.Title)
			for _, bullet := range role.Bullets {
				fmt.Fprintf(&b, "      - %s\n", bullet)
			}
		}
	}

	b.WriteString(`
## Output Format
Return ONLY this JSON object — no markdown, no explanation, no other text:
{
  "dimensions": {
    "skills": <integer 0-100>,
    "seniority": <integer 0-100>,
    "domain": <integer 0-100>,
    "title": <integer 0-100>
  },
  "gaps": [<exactly 5 strings: top gaps ranked by impact on score>],
  "strengths": [<exactly 5 strings: top strengths ranked by relevance>],
  "rationale": "<one sentence summarizing the overall fit>"
}

Scoring rubric:
- skills (40% weight): coverage of required skills; partial matches count proportionally
- seniority (25% weight): years and scope of experience vs role level
- domain (25% weight): industry and technical domain overlap
- title (10% weight): alignment of past titles with target role title
`)

	return b.String()
}

// parseScoringResponse extracts an LLMScoreResponse from raw claude CLI text output.
// Handles markdown code fences and leading/trailing prose.
func parseScoringResponse(raw []byte) (*LLMScoreResponse, error) {
	text := strings.TrimSpace(string(raw))

	// Strip markdown code fence if present (```json ... ``` or ``` ... ```)
	if strings.HasPrefix(text, "```") {
		if i := strings.Index(text, "\n"); i >= 0 {
			text = text[i+1:]
		}
		if i := strings.LastIndex(text, "```"); i >= 0 {
			text = text[:i]
		}
		text = strings.TrimSpace(text)
	}

	// Extract outermost JSON object
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object in response")
	}
	text = text[start : end+1]

	var resp LLMScoreResponse
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		return nil, err
	}
	if resp.Rationale == "" {
		return nil, fmt.Errorf("missing rationale field")
	}

	return &resp, nil
}
