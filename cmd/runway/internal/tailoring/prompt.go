package tailoring

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/thebrianlopez/runabout/cmd/runway/internal/scoring"
)

// buildTailorPrompt constructs the LLM tailoring prompt.
// No API keys, credentials, or email addresses are included in the output.
// Gaps and strengths come from MatchResult, not JobDescription.
func buildTailorPrompt(opts TailorOpts) string {
	var b strings.Builder

	b.WriteString("You are tailoring a resume YAML for a specific job application.\n")
	b.WriteString("Return ONLY the complete tailored resume.yaml content — no explanation, no markdown.\n\n")

	// JD context (XML-wrapped per TDD §4 prompt design)
	b.WriteString("<job_description>\n")
	fmt.Fprintf(&b, "  <title>%s</title>\n", opts.JD.Title)
	fmt.Fprintf(&b, "  <company>%s</company>\n", opts.JD.Company)
	fmt.Fprintf(&b, "  <seniority>%s</seniority>\n", opts.JD.SeniorityLevel)
	if len(opts.JD.Domain) > 0 {
		fmt.Fprintf(&b, "  <domain>%s</domain>\n", strings.Join(opts.JD.Domain, ", "))
	}
	fmt.Fprintf(&b, "  <required_skills>%s</required_skills>\n", strings.Join(opts.JD.RequiredSkills, ", "))
	if len(opts.JD.PreferredSkills) > 0 {
		fmt.Fprintf(&b, "  <preferred_skills>%s</preferred_skills>\n", strings.Join(opts.JD.PreferredSkills, ", "))
	}
	b.WriteString("</job_description>\n\n")

	// Match context: gaps/strengths from MatchResult
	fmt.Fprintf(&b, "<match_result overall_score=\"%d\" verdict=\"%s\">\n", opts.Result.OverallScore, opts.Result.Verdict)
	if len(opts.Result.Gaps) > 0 {
		fmt.Fprintf(&b, "  <gaps>%s</gaps>\n", strings.Join(opts.Result.Gaps, "; "))
	}
	if len(opts.Result.Strengths) > 0 {
		fmt.Fprintf(&b, "  <strengths>%s</strengths>\n", strings.Join(opts.Result.Strengths, "; "))
	}
	b.WriteString("</match_result>\n\n")

	b.WriteString("<instructions>\n")
	b.WriteString("Permitted mutations ONLY — everything else must be byte-for-byte identical to source:\n")
	b.WriteString("  1. skills: reorder categories to front-load JD-relevant skills; no categories added or removed\n")
	b.WriteString("  2. summary: reframe to lead with JD domain; ≤3 sentences changed; character count within ±15% of original\n")
	b.WriteString("  3. experience[*].bullets: reorder within a role to surface JD-relevant bullets first; no bullets added or removed\n")
	b.WriteString("NEVER fabricate new bullets, skills, or experience entries.\n")
	b.WriteString("</instructions>\n\n")

	// Source resume YAML (resume content included for tailoring; not logged by this package)
	b.WriteString("<source_resume_yaml>\n")
	b.WriteString(marshalResumeYAML(opts.Resume))
	b.WriteString("</source_resume_yaml>\n")

	return b.String()
}

func marshalResumeYAML(r *scoring.Resume) string {
	out, err := yaml.Marshal(r)
	if err != nil {
		return ""
	}
	return string(out)
}
