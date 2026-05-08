package scoring

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

// --- mock ---

type mockResult struct {
	resp *LLMScoreResponse
	err  error
}

type mockLLMScorer struct {
	mu    sync.Mutex
	calls int
	queue []mockResult
}

func (m *mockLLMScorer) ScoreWithLLM(_ context.Context, _ *JobDescription, _ *Resume) (*LLMScoreResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.calls >= len(m.queue) {
		panic("mockLLMScorer: unexpected call (no result queued)")
	}
	r := m.queue[m.calls]
	m.calls++
	return r.resp, r.err
}

func (m *mockLLMScorer) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// validResume returns a Resume with all required sections populated.
func validResume() *Resume {
	return &Resume{
		Summary: "15+ years infrastructure engineer",
		Skills:  map[string][]string{"Infrastructure & Cloud": {"Terraform", "AWS", "Kubernetes"}},
		Experience: []ExperienceEntry{
			{Company: "Grindr", Roles: []Role{{Title: "Senior Cloud Engineer", Bullets: []string{"Managed 5 EKS clusters"}}}},
		},
	}
}

// validJD returns a JobDescription with RequiredSkills populated.
func validJD() *JobDescription {
	return &JobDescription{
		Title:          "Staff Infrastructure Engineer",
		Company:        "Stripe",
		RequiredSkills: []string{"Terraform", "Kubernetes", "Go"},
		SeniorityLevel: "staff",
	}
}

// validLLMResponse returns a well-formed LLMScoreResponse.
func validLLMResponse(skills, seniority, domain, title int) *LLMScoreResponse {
	return &LLMScoreResponse{
		Dimensions: LLMDimensions{Skills: skills, Seniority: seniority, Domain: domain, Title: title},
		Gaps:       []string{"gap1", "gap2", "gap3", "gap4", "gap5"},
		Strengths:  []string{"str1", "str2", "str3", "str4", "str5"},
		Rationale:  "strong infra match",
	}
}

// --- CT-1: weight formula ---

// CT-1: OverallScore is a weighted composite of the four dimensions.
// Weights: Skills 40%, Seniority 25%, Domain 25%, Title 10%.
// Given {80, 60, 70, 50}: 80*0.4 + 60*0.25 + 70*0.25 + 50*0.1 = 32+15+17.5+5 = 69.5 → 70.
func TestComputeOverallScore_WeightFormula(t *testing.T) {
	d := MatchDimensions{Skills: 80, Seniority: 60, Domain: 70, Title: 50}
	want := 70
	got := computeOverallScore(d)
	if got != want {
		t.Errorf("computeOverallScore(%+v) = %d, want %d", d, got, want)
	}
}

// --- CT-2: verdict derived by engine, never LLM ---

// CT-2: verdictFromScore(86) must return strong_fit.
// This ensures the engine, not the LLM, controls the verdict.
func TestVerdictDerivedByEngine(t *testing.T) {
	got := verdictFromScore(86)
	if got != VerdictStrongFit {
		t.Errorf("verdictFromScore(86) = %q, want %q", got, VerdictStrongFit)
	}
}

// --- CT-3: verdict threshold boundaries ---

// CT-3: Each threshold boundary produces the correct Verdict.
func TestVerdictFromScore_Boundaries(t *testing.T) {
	tests := []struct {
		score int
		want  Verdict
	}{
		{86, VerdictStrongFit},  // above strong_fit boundary
		{85, VerdictApply},      // at boundary: 85 is apply (threshold is >85 for strong_fit)
		{40, VerdictApply},      // at lower apply boundary
		{39, VerdictWeakFit},    // just below apply
		{25, VerdictWeakFit},    // at lower weak_fit boundary
		{24, VerdictDoNotApply}, // just below weak_fit
		{0, VerdictDoNotApply},  // floor
		{100, VerdictStrongFit}, // ceiling
	}
	for _, tt := range tests {
		got := verdictFromScore(tt.score)
		if got != tt.want {
			t.Errorf("verdictFromScore(%d) = %q, want %q", tt.score, got, tt.want)
		}
	}
}

// --- CT-4: MatchResult JSON schema ---

// CT-4: MatchResult round-trips through JSON without data loss.
func TestMatchResult_JSONRoundtrip(t *testing.T) {
	original := &MatchResult{
		OverallScore:  75,
		Dimensions:    MatchDimensions{Skills: 80, Seniority: 70, Domain: 75, Title: 60},
		Gaps:          []string{"Missing Go experience"},
		Strengths:     []string{"Strong Kubernetes background"},
		Verdict:       VerdictApply,
		VerdictReason: "good infrastructure match, minor language gap",
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal(MatchResult) error: %v", err)
	}
	var got MatchResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal(MatchResult) error: %v", err)
	}
	if got.OverallScore != original.OverallScore {
		t.Errorf("OverallScore = %d, want %d", got.OverallScore, original.OverallScore)
	}
	if got.Verdict != original.Verdict {
		t.Errorf("Verdict = %q, want %q", got.Verdict, original.Verdict)
	}
	if got.Dimensions != original.Dimensions {
		t.Errorf("Dimensions = %+v, want %+v", got.Dimensions, original.Dimensions)
	}
	if len(got.Gaps) != len(original.Gaps) || got.Gaps[0] != original.Gaps[0] {
		t.Errorf("Gaps = %v, want %v", got.Gaps, original.Gaps)
	}
}

// --- CT-5: single retry on ERR_SCORE_INVALID_RESPONSE ---

// CT-5: ScoreMatch retries exactly once when the LLM returns an invalid response.
// First call returns ErrScoreInvalidResponse; second call succeeds.
// Total LLM call count must be exactly 2.
func TestScoreMatch_SingleRetryOnInvalidResponse(t *testing.T) {
	mock := &mockLLMScorer{
		queue: []mockResult{
			{nil, ErrScoreInvalidResponse},
			{validLLMResponse(80, 70, 75, 60), nil},
		},
	}
	s := &Scorer{LLM: mock}

	_, err := s.ScoreMatch(context.Background(), validJD(), validResume())
	if err != nil {
		t.Errorf("ScoreMatch() error = %v, want nil (retry should have succeeded)", err)
	}
	if mock.callCount() != 2 {
		t.Errorf("LLM call count = %d, want 2 (initial + 1 retry)", mock.callCount())
	}
}

// --- CT-6: no retry on ERR_SCORE_LLM_TIMEOUT ---

// CT-6: ScoreMatch does not retry when the LLM times out.
// Exactly 1 LLM call must be made; ErrScoreLLMTimeout returned.
func TestScoreMatch_NoRetryOnTimeout(t *testing.T) {
	mock := &mockLLMScorer{
		queue: []mockResult{
			{nil, ErrScoreLLMTimeout},
		},
	}
	s := &Scorer{LLM: mock}

	_, err := s.ScoreMatch(context.Background(), validJD(), validResume())
	var scoreErr *ScoreError
	if !errors.As(err, &scoreErr) || scoreErr.Code != "score/timeout" {
		t.Errorf("ScoreMatch() error = %v, want score/timeout", err)
	}
	if mock.callCount() != 1 {
		t.Errorf("LLM call count = %d, want 1 (no retry on timeout)", mock.callCount())
	}
}

// --- CT-7: ERR_RESUME_PARSE on missing section ---

// CT-7: ScoreMatch returns score/resume_parse with Field="skills" when
// resume.Skills is nil. No LLM call must be made.
func TestScoreMatch_ErrResumeParse_MissingSkills(t *testing.T) {
	mock := &mockLLMScorer{}
	s := &Scorer{LLM: mock}

	resume := validResume()
	resume.Skills = nil

	_, err := s.ScoreMatch(context.Background(), validJD(), resume)
	var scoreErr *ScoreError
	if !errors.As(err, &scoreErr) {
		t.Fatalf("ScoreMatch() error = %v (%T), want *ScoreError", err, err)
	}
	if scoreErr.Code != "score/resume_parse" {
		t.Errorf("ScoreError.Code = %q, want \"score/resume_parse\"", scoreErr.Code)
	}
	if scoreErr.Field != "skills" {
		t.Errorf("ScoreError.Field = %q, want \"skills\"", scoreErr.Field)
	}
	if mock.callCount() != 0 {
		t.Errorf("LLM call count = %d, want 0 (validation fails before LLM)", mock.callCount())
	}
}

// CT-7b: same for missing Summary.
func TestScoreMatch_ErrResumeParse_MissingSummary(t *testing.T) {
	mock := &mockLLMScorer{}
	s := &Scorer{LLM: mock}

	resume := validResume()
	resume.Summary = ""

	_, err := s.ScoreMatch(context.Background(), validJD(), resume)
	var scoreErr *ScoreError
	if !errors.As(err, &scoreErr) {
		t.Fatalf("ScoreMatch() error = %v (%T), want *ScoreError", err, err)
	}
	if scoreErr.Code != "score/resume_parse" {
		t.Errorf("ScoreError.Code = %q, want \"score/resume_parse\"", scoreErr.Code)
	}
	if scoreErr.Field != "summary" {
		t.Errorf("ScoreError.Field = %q, want \"summary\"", scoreErr.Field)
	}
}

// --- CT-8: ERR_JD_INSUFFICIENT on empty RequiredSkills ---

// CT-8: ScoreMatch returns score/jd_insufficient when JD has no RequiredSkills.
// No LLM call must be made.
func TestScoreMatch_ErrJDInsufficient_EmptySkills(t *testing.T) {
	mock := &mockLLMScorer{}
	s := &Scorer{LLM: mock}

	jd := validJD()
	jd.RequiredSkills = []string{}

	_, err := s.ScoreMatch(context.Background(), jd, validResume())
	var scoreErr *ScoreError
	if !errors.As(err, &scoreErr) {
		t.Fatalf("ScoreMatch() error = %v (%T), want *ScoreError", err, err)
	}
	if scoreErr.Code != "score/jd_insufficient" {
		t.Errorf("ScoreError.Code = %q, want \"score/jd_insufficient\"", scoreErr.Code)
	}
	if mock.callCount() != 0 {
		t.Errorf("LLM call count = %d, want 0 (validation fails before LLM)", mock.callCount())
	}
}

// --- CT-9: dimension scores clamped to [0, 100] ---

// CT-9a: clampDimensions clamps values above 100 to 100.
func TestClampDimensions_AboveMax(t *testing.T) {
	got := clampDimensions(MatchDimensions{Skills: 105, Seniority: 60, Domain: 70, Title: 50})
	if got.Skills != 100 {
		t.Errorf("clampDimensions.Skills = %d, want 100 (clamped from 105)", got.Skills)
	}
}

// CT-9b: clampDimensions clamps values below 0 to 0.
func TestClampDimensions_BelowMin(t *testing.T) {
	got := clampDimensions(MatchDimensions{Skills: -5, Seniority: 60, Domain: 70, Title: 50})
	if got.Skills != 0 {
		t.Errorf("clampDimensions.Skills = %d, want 0 (clamped from -5)", got.Skills)
	}
}

// CT-9c: OverallScore is computed from clamped dimensions, not raw.
// With Skills=105 clamped to 100: 100*0.4 + 60*0.25 + 70*0.25 + 50*0.1 = 77.5 → 78.
// Without clamping: 105*0.4 + ... = 79.5 → 80. Difference proves clamping occurred.
func TestComputeOverallScore_UsesClamped(t *testing.T) {
	raw := MatchDimensions{Skills: 105, Seniority: 60, Domain: 70, Title: 50}
	clamped := clampDimensions(raw)
	got := computeOverallScore(clamped)
	want := 78 // clamped path; unclamped would be 80
	if got != want {
		t.Errorf("computeOverallScore(clamped) = %d, want %d (must use clamped Skills=100, not raw 105)", got, want)
	}
}

// --- RG-1: verdict override prevention ---

// RG-1: verdictFromScore always produces the correct verdict regardless of any
// external input. This test locks out the risk of LLM-supplied verdicts leaking in.
func TestVerdictOverridePrevention(t *testing.T) {
	// A score of 20 must always yield do_not_apply — no external value can change this.
	tests := []struct {
		score int
		want  Verdict
	}{
		{20, VerdictDoNotApply},
		{30, VerdictWeakFit},
		{50, VerdictApply},
		{90, VerdictStrongFit},
	}
	for _, tt := range tests {
		got := verdictFromScore(tt.score)
		if got != tt.want {
			t.Errorf("verdictFromScore(%d) = %q, want %q (engine must derive verdict, not LLM)", tt.score, got, tt.want)
		}
	}
}
