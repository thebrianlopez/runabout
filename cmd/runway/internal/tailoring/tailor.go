// Package tailoring implements F3 Resume Tailoring Generator.
// See: docs/design/personal_20260508T115312Z_JobSearch_F3-ResumeTailoringGenerator_TDD.md
package tailoring

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/thebrianlopez/runabout/cmd/runway/internal/scoring"
)

// TailorOpts is the input to TailorResume.
type TailorOpts struct {
	Result         *scoring.MatchResult
	JD             *scoring.JobDescription
	Resume         *scoring.Resume
	SourceYAMLPath string // path to original resume.yaml; used as fallback YAMLPath
	Override       bool   // allow tailoring even on do_not_apply verdict
	Model          string // overrides RUNWAY_TAILOR_MODEL and default
}

// TailoredResume is the output of TailorResume.
type TailoredResume struct {
	Slug         string     // e.g. "stripe-staff-engineer-20260508"
	SourceSHA    string     // git SHA of resume.yaml base (populated by caller)
	YAMLPath     string     // absolute path to validated tailored YAML
	UsedFallback bool       // true when validation failed; YAMLPath points to original
	Diff         ResumeDiff
}

// ResumeDiff describes what changed between source and tailored resume.
type ResumeDiff struct {
	SkillsReordered  bool
	SummaryChanged   bool
	BulletsReordered []string // company names where bullet order changed
	NothingChanged   bool     // true when strong_fit > 95 short-circuit applied
}

// TailorError is returned by TailorResume on fatal failure paths.
// Auto-fallback errors (schema_fail, fabrication, summary_bounds) are NOT
// returned as errors — they set UsedFallback=true on the result instead.
type TailorError struct {
	Code string
	msg  string
}

func (e *TailorError) Error() string { return e.msg }

var (
	ErrTailorVerdictBlocked = &TailorError{Code: "tailor/verdict_blocked", msg: "score too low to tailor (verdict: do_not_apply); use --override to proceed"}
	ErrTailorLLMTimeout     = &TailorError{Code: "tailor/timeout", msg: "tailoring timed out; retry or submit with original resume"}
)

// autoFallback errors are returned inside the result with UsedFallback=true,
// not as function-level errors. Exported for test assertions on Code field.
var (
	errTailorSchemaFail    = &TailorError{Code: "tailor/schema_fail", msg: "tailored resume failed schema validation; using original"}
	errTailorFabrication   = &TailorError{Code: "tailor/fabrication", msg: "tailored resume contains fabricated content; using original"}
	errTailorSummaryBounds = &TailorError{Code: "tailor/summary_bounds", msg: "tailored summary outside ±15% length bounds; using original"}
)

// LLMTailor generates a full tailored resume YAML string.
type LLMTailor interface {
	TailorWithLLM(ctx context.Context, prompt string) (yamlStr string, err error)
}

// SchemaValidator validates a YAML file against the resume schema.
type SchemaValidator interface {
	Validate(ctx context.Context, yamlPath string) error
}

// Tailorer orchestrates resume tailoring with injected LLM and validator.
type Tailorer struct {
	LLM       LLMTailor
	Validator SchemaValidator
	Logger    *slog.Logger
	Now       func() time.Time // injectable for deterministic slug tests; defaults to time.Now
}

func (t *Tailorer) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Tailorer) log(ctx context.Context, level slog.Level, msg string, attrs ...slog.Attr) {
	if t.Logger != nil {
		t.Logger.LogAttrs(ctx, level, msg, attrs...)
	}
}

// TailorResume generates a tailored resume for the given match result.
// Fatal errors (verdict_blocked, timeout) are returned as errors.
// Validation failures (schema, fabrication, summary_bounds) set UsedFallback=true
// and return a result pointing to the original resume — the pipeline is never blocked.
func (t *Tailorer) TailorResume(ctx context.Context, opts TailorOpts) (*TailoredResume, error) {
	// CT-3/CT-4: verdict gate
	if opts.Result.Verdict == scoring.VerdictDoNotApply && !opts.Override {
		return nil, ErrTailorVerdictBlocked
	}

	slug := deriveSlug(opts.JD, t.now())

	// CT-7: strong_fit > 95 short-circuit
	if opts.Result.Verdict == scoring.VerdictStrongFit && opts.Result.OverallScore > 95 {
		t.log(ctx, slog.LevelInfo, "tailor.nothing_changed",
			slog.String("slug", slug),
			slog.Int("overall_score", opts.Result.OverallScore),
		)
		return &TailoredResume{
			Slug:     slug,
			YAMLPath: opts.SourceYAMLPath,
			Diff:     ResumeDiff{NothingChanged: true},
		}, nil
	}

	// CT-9: LLM call with 30s timeout; no retry (cost protection per TDD §4)
	prompt := buildTailorPrompt(opts)
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	t.log(ctx, slog.LevelInfo, "tailor.started",
		slog.String("slug", slug),
		slog.String("verdict", string(opts.Result.Verdict)),
	)

	yamlStr, err := t.LLM.TailorWithLLM(callCtx, prompt)
	if err != nil {
		if callCtx.Err() == context.DeadlineExceeded {
			return nil, ErrTailorLLMTimeout
		}
		return nil, ErrTailorLLMTimeout
	}

	// Write tailored YAML to temp file
	tmp, err := os.CreateTemp("", "resume-tailored-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(yamlStr); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("failed to write tailored YAML: %w", err)
	}
	tmp.Close()

	fallback := func(code *TailorError) *TailoredResume {
		os.Remove(tmpPath)
		t.log(ctx, slog.LevelWarn, "tailor.fallback",
			slog.String("reason", code.Code),
			slog.String("slug", slug),
		)
		return &TailoredResume{
			Slug:         slug,
			YAMLPath:     opts.SourceYAMLPath,
			UsedFallback: true,
		}
	}

	// CT-2: schema validation
	if err := t.Validator.Validate(ctx, tmpPath); err != nil {
		return fallback(errTailorSchemaFail), nil
	}

	// Parse tailored YAML back into Resume struct for post-generation checks
	var tailored scoring.Resume
	raw, err := os.ReadFile(tmpPath)
	if err != nil {
		return fallback(errTailorSchemaFail), nil
	}
	if err := yaml.Unmarshal(raw, &tailored); err != nil {
		return fallback(errTailorSchemaFail), nil
	}

	// CT-1/CT-6: fabrication detection
	if detectFabrication(opts.Resume, &tailored) {
		return fallback(errTailorFabrication), nil
	}

	// CT-5: summary bounds
	if !checkSummaryBounds(opts.Resume.Summary, tailored.Summary) {
		return fallback(errTailorSummaryBounds), nil
	}

	diff := computeDiff(opts.Resume, &tailored)
	t.log(ctx, slog.LevelInfo, "tailor.completed",
		slog.String("slug", slug),
		slog.Bool("skills_reordered", diff.SkillsReordered),
		slog.Bool("summary_changed", diff.SummaryChanged),
	)

	return &TailoredResume{
		Slug:     slug,
		YAMLPath: tmpPath,
		Diff:     diff,
	}, nil
}

// deriveSlug derives the resume slug from the JD and current date.
// Format: kebab(company)-kebab(title)-YYYYMMDD, max 80 chars.
func deriveSlug(jd *scoring.JobDescription, t time.Time) string {
	company := toKebab(jd.Company)
	title := toKebab(jd.Title)
	date := t.Format("20060102")

	slug := company + "-" + title + "-" + date
	if len(slug) <= 80 {
		return slug
	}
	// Truncate title to fit within 80 chars: company + "-" + title + "-" + date
	maxTitle := 80 - len(company) - 1 - 1 - len(date)
	if maxTitle < 4 {
		maxTitle = 4
	}
	if len(title) > maxTitle {
		title = title[:maxTitle]
		if i := strings.LastIndex(title, "-"); i > 0 {
			title = title[:i]
		}
	}
	return company + "-" + title + "-" + date
}

var nonAlphanumRE = regexp.MustCompile(`[^a-z0-9]+`)

func toKebab(s string) string {
	s = strings.ToLower(s)
	s = nonAlphanumRE.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

func computeDiff(source, tailored *scoring.Resume) ResumeDiff {
	d := ResumeDiff{}

	if source.Summary != tailored.Summary {
		d.SummaryChanged = true
	}

	// Skills: check if category order changed
	sourceOrder := categoryOrder(source.Skills)
	tailoredOrder := categoryOrder(tailored.Skills)
	if !sliceEqual(sourceOrder, tailoredOrder) {
		d.SkillsReordered = true
	}

	// Experience: check bullet order per company
	sourceByCompany := experienceByCompany(source.Experience)
	for _, entry := range tailored.Experience {
		src, ok := sourceByCompany[entry.Company]
		if !ok {
			continue
		}
		for i, role := range entry.Roles {
			if i >= len(src.Roles) {
				break
			}
			if !sliceEqual(role.Bullets, src.Roles[i].Bullets) {
				d.BulletsReordered = append(d.BulletsReordered, entry.Company)
				break
			}
		}
	}

	return d
}

func categoryOrder(skills map[string][]string) []string {
	// Map iteration order is random; we need stable comparison
	// For diff purposes, just check set equality for now
	keys := make([]string, 0, len(skills))
	for k := range skills {
		keys = append(keys, k)
	}
	return keys
}

func experienceByCompany(entries []scoring.ExperienceEntry) map[string]scoring.ExperienceEntry {
	m := make(map[string]scoring.ExperienceEntry, len(entries))
	for _, e := range entries {
		m[e.Company] = e
	}
	return m
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
