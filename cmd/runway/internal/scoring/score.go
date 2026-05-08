// Package scoring implements resume-to-job-description match scoring.
// See: docs/design/personal_20260508T114736Z_JobSearch_F2-MatchScoringEngine_TDD.md
package scoring

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"
)

// Verdict classifies a resume-to-JD match.
type Verdict string

const (
	VerdictStrongFit  Verdict = "strong_fit"   // OverallScore > 85
	VerdictApply      Verdict = "apply"         // OverallScore 40–85
	VerdictWeakFit    Verdict = "weak_fit"      // OverallScore 25–39
	VerdictDoNotApply Verdict = "do_not_apply"  // OverallScore < 25
)

// MatchDimensions holds per-axis scores, each in [0, 100].
type MatchDimensions struct {
	Skills    int `json:"skills"`
	Seniority int `json:"seniority"`
	Domain    int `json:"domain"`
	Title     int `json:"title"`
}

// MatchResult is the output of ScoreMatch.
// Verdict is always computed by the engine — never accepted from LLM output.
type MatchResult struct {
	OverallScore  int             `json:"overall_score"`
	Dimensions    MatchDimensions `json:"dimensions"`
	Gaps          []string        `json:"gaps"`
	Strengths     []string        `json:"strengths"`
	Verdict       Verdict         `json:"verdict"`
	VerdictReason string          `json:"verdict_reason"`
}

// JobDescription is the parsed representation of a job posting.
type JobDescription struct {
	Title           string   `json:"title"`
	Company         string   `json:"company"`
	SourceURL       string   `json:"source_url,omitempty"`
	RequiredSkills  []string `json:"required_skills"`
	PreferredSkills []string `json:"preferred_skills,omitempty"`
	SeniorityLevel  string   `json:"seniority_level"`
	Domain          []string `json:"domain,omitempty"`
	RawText         string   `json:"raw_text"`
}

// Resume holds the structured content of resume.yaml relevant to scoring.
type Resume struct {
	Summary    string              `yaml:"summary"`
	Skills     map[string][]string `yaml:"skills"`
	Experience []ExperienceEntry   `yaml:"experience"`
}

// ExperienceEntry represents one employer block.
type ExperienceEntry struct {
	Company string `yaml:"company"`
	Roles   []Role `yaml:"roles"`
}

// Role is a position within an ExperienceEntry.
type Role struct {
	Title   string   `yaml:"title"`
	Bullets []string `yaml:"bullets"`
}

// LLMDimensions holds raw dimension scores returned by the LLM before clamping.
type LLMDimensions struct {
	Skills    int `json:"skills"`
	Seniority int `json:"seniority"`
	Domain    int `json:"domain"`
	Title     int `json:"title"`
}

// LLMScoreResponse is the expected structured JSON output from the LLM scorer.
type LLMScoreResponse struct {
	Dimensions LLMDimensions `json:"dimensions"`
	Gaps       []string      `json:"gaps"`
	Strengths  []string      `json:"strengths"`
	Rationale  string        `json:"rationale"`
}

// LLMScorer is the interface for LLM-based dimension scoring.
// The production implementation shells out to the claude CLI.
// Tests inject a mock.
type LLMScorer interface {
	ScoreWithLLM(ctx context.Context, jd *JobDescription, resume *Resume) (*LLMScoreResponse, error)
}

// MetricsRecorder emits scoring metrics. The nil value is safe — Scorer
// skips recording when Metrics is nil.
type MetricsRecorder interface {
	RecordScoreLatency(verdict string, attempt int, latencyMs int64)
	RecordScoreError(errorClass string)
	RecordVerdict(verdict string)
	RecordRetry()
}

// Scorer orchestrates match scoring with an injected LLMScorer.
type Scorer struct {
	LLM     LLMScorer
	Logger  *slog.Logger  // nil disables logging
	Metrics MetricsRecorder // nil disables metrics
}

// ScoreError is returned by ScoreMatch on all failure paths.
// Code matches the taxonomy in TDD §4.
type ScoreError struct {
	Code  string // e.g. "score/timeout"
	Field string // set for score/resume_parse errors
	msg   string
}

func (e *ScoreError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("%s: field=%s", e.msg, e.Field)
	}
	return e.msg
}

// Sentinel errors. Use errors.As to inspect Code and Field.
var (
	ErrScoreLLMTimeout      = &ScoreError{Code: "score/timeout", msg: "scoring timed out"}
	ErrScoreInvalidResponse = &ScoreError{Code: "score/invalid_response", msg: "scoring returned unexpected format after retry"}
)

func errResumeParse(field string) *ScoreError {
	return &ScoreError{Code: "score/resume_parse", Field: field, msg: "resume.yaml failed validation"}
}

func errJDInsufficient() *ScoreError {
	return &ScoreError{Code: "score/jd_insufficient", msg: "job description has no parseable skill requirements"}
}

// ScoreMatch scores resume against jd and returns a MatchResult.
// Validates inputs, calls LLM (with one retry on invalid response),
// clamps raw dimension scores, computes weighted composite, derives Verdict.
//
// Verdict is always computed by this function — the LLM rationale field is
// used for VerdictReason but the verdict enum is never trusted from LLM output.
func (s *Scorer) ScoreMatch(ctx context.Context, jd *JobDescription, resume *Resume) (*MatchResult, error) {
	start := time.Now()
	attempt := 1

	s.logAttrs(ctx, slog.LevelInfo, "score.started",
		slog.String("jd_title", jd.Title),
		slog.String("jd_company", jd.Company),
	)

	if resume.Summary == "" {
		return nil, errResumeParse("summary")
	}
	if resume.Skills == nil {
		return nil, errResumeParse("skills")
	}
	if len(jd.RequiredSkills) == 0 {
		return nil, errJDInsufficient()
	}

	llmResp, err := s.LLM.ScoreWithLLM(ctx, jd, resume)
	if err != nil {
		if errors.Is(err, ErrScoreInvalidResponse) {
			s.logAttrs(ctx, slog.LevelWarn, "score.retry",
				slog.Int("attempt", attempt),
				slog.String("error_class", "score/invalid_response"),
			)
			if s.Metrics != nil {
				s.Metrics.RecordRetry()
			}
			attempt = 2
			llmResp, err = s.LLM.ScoreWithLLM(ctx, jd, resume)
		}
		if err != nil {
			latencyMs := time.Since(start).Milliseconds()
			var scoreErr *ScoreError
			if errors.As(err, &scoreErr) {
				s.logAttrs(ctx, slog.LevelError, "score.failed",
					slog.String("error_class", scoreErr.Code),
					slog.Int("attempt", attempt),
					slog.Int64("latency_ms", latencyMs),
				)
				if s.Metrics != nil {
					s.Metrics.RecordScoreError(scoreErr.Code)
				}
			}
			return nil, err
		}
	}

	dims := clampDimensions(MatchDimensions{
		Skills:    llmResp.Dimensions.Skills,
		Seniority: llmResp.Dimensions.Seniority,
		Domain:    llmResp.Dimensions.Domain,
		Title:     llmResp.Dimensions.Title,
	})
	overall := computeOverallScore(dims)
	result := &MatchResult{
		OverallScore:  overall,
		Dimensions:    dims,
		Gaps:          llmResp.Gaps,
		Strengths:     llmResp.Strengths,
		Verdict:       verdictFromScore(overall),
		VerdictReason: llmResp.Rationale,
	}

	latencyMs := time.Since(start).Milliseconds()
	s.logAttrs(ctx, slog.LevelInfo, "score.completed",
		slog.Int("overall_score", result.OverallScore),
		slog.String("verdict", string(result.Verdict)),
		slog.Int64("latency_ms", latencyMs),
		slog.Int("attempt", attempt),
	)
	if s.Metrics != nil {
		s.Metrics.RecordScoreLatency(string(result.Verdict), attempt, latencyMs)
		s.Metrics.RecordVerdict(string(result.Verdict))
	}

	return result, nil
}

func (s *Scorer) logAttrs(ctx context.Context, level slog.Level, msg string, attrs ...slog.Attr) {
	if s.Logger != nil {
		s.Logger.LogAttrs(ctx, level, msg, attrs...)
	}
}

// computeOverallScore returns the weighted composite of clamped dimensions.
// Weights: Skills 40%, Seniority 25%, Domain 25%, Title 10%.
// Caller must clamp dimensions before passing.
func computeOverallScore(d MatchDimensions) int {
	weighted := float64(d.Skills)*0.40 + float64(d.Seniority)*0.25 + float64(d.Domain)*0.25 + float64(d.Title)*0.10
	return int(math.Round(weighted))
}

// verdictFromScore derives the Verdict enum from an overall score.
func verdictFromScore(score int) Verdict {
	switch {
	case score > 85:
		return VerdictStrongFit
	case score >= 40:
		return VerdictApply
	case score >= 25:
		return VerdictWeakFit
	default:
		return VerdictDoNotApply
	}
}

// clampDimensions clamps each dimension value to [0, 100].
func clampDimensions(d MatchDimensions) MatchDimensions {
	return MatchDimensions{
		Skills:    clamp(d.Skills, 0, 100),
		Seniority: clamp(d.Seniority, 0, 100),
		Domain:    clamp(d.Domain, 0, 100),
		Title:     clamp(d.Title, 0, 100),
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
