package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/thebrianlopez/runabout/cmd/runway/internal/scoring"
)

const (
	defaultExtractModel   = "claude-haiku-4-5-20251001"
	defaultExtractTimeout = 10 * time.Second
)

// ClaudeLLMExtractor implements JDExtractor by calling the claude CLI.
// Uses the same OAuth2 session as the rest of the claude CLI toolchain.
type ClaudeLLMExtractor struct {
	Model   string
	Timeout time.Duration
}

func (c *ClaudeLLMExtractor) model() string {
	if c.Model != "" {
		return c.Model
	}
	return defaultExtractModel
}

func (c *ClaudeLLMExtractor) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return defaultExtractTimeout
}

// ExtractJD calls Claude to extract structured JobDescription fields from raw text.
// Returns ErrJDParseFailed on malformed or incomplete LLM output.
func (c *ClaudeLLMExtractor) ExtractJD(ctx context.Context, text string) (*scoring.JobDescription, error) {
	prompt := buildExtractionPrompt(text)

	callCtx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	cmd := exec.CommandContext(callCtx, "claude", "-p", prompt, "--model", c.model())
	out, err := cmd.Output()
	if err != nil {
		return nil, ErrJDParseFailed
	}

	jd, err := parseExtractionResponse(out)
	if err != nil {
		return nil, ErrJDParseFailed
	}
	return jd, nil
}

// buildExtractionPrompt constructs the extraction prompt from raw job description text.
// No credentials or PII beyond what's in the job posting itself are included.
func buildExtractionPrompt(text string) string {
	var b strings.Builder
	b.WriteString("Extract structured fields from the following job description.\n")
	b.WriteString("Return ONLY the JSON object below — no markdown, no explanation.\n\n")
	b.WriteString("## Job Description Text\n")
	b.WriteString(text)
	b.WriteString(`

## Required Output Format
{
  "title": "<job title>",
  "company": "<company name>",
  "required_skills": [<list of required technical skills>],
  "preferred_skills": [<list of preferred/nice-to-have skills, may be empty>],
  "seniority_level": "<one of: junior | mid | senior | staff | principal | executive>",
  "domain": [<list of technical domains, e.g. "infrastructure", "machine-learning", "platform">]
}

Rules:
- required_skills: only skills explicitly marked required or clearly implied as mandatory
- preferred_skills: skills marked preferred, nice-to-have, or bonus
- seniority_level: infer from title, years of experience, and scope; default to "senior" if unclear
- domain: 1-4 domain tags; use lowercase hyphenated strings
`)
	return b.String()
}

type extractionResponse struct {
	Title           string   `json:"title"`
	Company         string   `json:"company"`
	RequiredSkills  []string `json:"required_skills"`
	PreferredSkills []string `json:"preferred_skills"`
	SeniorityLevel  string   `json:"seniority_level"`
	Domain          []string `json:"domain"`
}

// parseExtractionResponse parses the LLM output into a JobDescription.
// Handles markdown code fences. Returns an error if required fields are missing.
func parseExtractionResponse(raw []byte) (*scoring.JobDescription, error) {
	text := strings.TrimSpace(string(raw))

	if strings.HasPrefix(text, "```") {
		if i := strings.Index(text, "\n"); i >= 0 {
			text = text[i+1:]
		}
		if i := strings.LastIndex(text, "```"); i >= 0 {
			text = text[:i]
		}
		text = strings.TrimSpace(text)
	}

	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object in extraction response")
	}
	text = text[start : end+1]

	var r extractionResponse
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		return nil, err
	}
	if len(r.RequiredSkills) == 0 {
		return nil, fmt.Errorf("extracted zero required skills")
	}

	return &scoring.JobDescription{
		Title:           r.Title,
		Company:         r.Company,
		RequiredSkills:  r.RequiredSkills,
		PreferredSkills: r.PreferredSkills,
		SeniorityLevel:  r.SeniorityLevel,
		Domain:          r.Domain,
	}, nil
}
