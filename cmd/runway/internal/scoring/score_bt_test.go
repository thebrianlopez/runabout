package scoring

// Behavioral tests (BT) and observability tests for the scoring package.
// Written during M4 implementation — all BT tests use canned mock responses
// (canned fixture pattern per TDD §6 FIRST Constraints).
//
// BT-6 (prompt PII check) tests buildScoringPrompt directly — no live CLI call needed.

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// --- BT helpers ---

func highMatchJD() *JobDescription {
	return &JobDescription{
		Title:          "Staff Infrastructure Engineer",
		Company:        "Stripe",
		RequiredSkills: []string{"Terraform", "Kubernetes", "AWS"},
		SeniorityLevel: "staff",
	}
}

func juniorResume() *Resume {
	return &Resume{
		Summary: "2 years infrastructure experience",
		Skills:  map[string][]string{"Cloud": {"AWS"}},
		Experience: []ExperienceEntry{
			{Company: "Startup", Roles: []Role{{Title: "Junior SRE", Bullets: []string{"managed small infra"}}}},
		},
	}
}

func mlJD() *JobDescription {
	return &JobDescription{
		Title:          "ML Infrastructure Engineer",
		Company:        "DeepMind",
		RequiredSkills: []string{"PyTorch", "CUDA", "distributed training"},
		SeniorityLevel: "senior",
		Domain:         []string{"machine_learning", "deep_learning"},
	}
}

// --- BT-1: high-match resume → Skills ≥ 80 ---

// BT-1: When resume covers all required JD skills, Skills dimension ≥ 80.
// Fixture: LLM returns Skills=90 for a full-match scenario.
func TestScoreMatch_BT1_HighMatchScoresSkillsGE80(t *testing.T) {
	mock := &mockLLMScorer{
		queue: []mockResult{
			{&LLMScoreResponse{
				Dimensions: LLMDimensions{Skills: 90, Seniority: 75, Domain: 70, Title: 65},
				Gaps:       []string{},
				Strengths:  []string{"all required skills present"},
				Rationale:  "strong technical match",
			}, nil},
		},
	}
	s := &Scorer{LLM: mock}
	result, err := s.ScoreMatch(context.Background(), highMatchJD(), validResume())
	if err != nil {
		t.Fatalf("ScoreMatch() error = %v", err)
	}
	if result.Dimensions.Skills < 80 {
		t.Errorf("Skills = %d, want ≥ 80 (high-match resume with all required skills)", result.Dimensions.Skills)
	}
}

// --- BT-2: seniority mismatch → Seniority ≤ 40 ---

// BT-2: Staff-level JD against junior resume scores Seniority ≤ 40.
// Fixture: LLM returns Seniority=35 for the seniority-gap scenario.
func TestScoreMatch_BT2_SeniorityMismatchPenalizesScore(t *testing.T) {
	mock := &mockLLMScorer{
		queue: []mockResult{
			{&LLMScoreResponse{
				Dimensions: LLMDimensions{Skills: 70, Seniority: 35, Domain: 60, Title: 55},
				Gaps:       []string{"junior experience vs staff requirement"},
				Strengths:  []string{"some cloud skills"},
				Rationale:  "seniority gap too large for staff role",
			}, nil},
		},
	}
	jd := &JobDescription{
		Title:          "Staff Infrastructure Engineer",
		Company:        "Stripe",
		RequiredSkills: []string{"Terraform"},
		SeniorityLevel: "staff",
	}
	s := &Scorer{LLM: mock}
	result, err := s.ScoreMatch(context.Background(), jd, juniorResume())
	if err != nil {
		t.Fatalf("ScoreMatch() error = %v", err)
	}
	if result.Dimensions.Seniority > 40 {
		t.Errorf("Seniority = %d, want ≤ 40 for junior resume vs staff-level JD", result.Dimensions.Seniority)
	}
}

// --- BT-3: domain mismatch → Domain ≤ 50 ---

// BT-3: ML-focused JD against infra resume scores Domain ≤ 50.
// Fixture: LLM returns Domain=40 for the cross-domain scenario.
func TestScoreMatch_BT3_DomainMismatchPenalizesDomainDimension(t *testing.T) {
	mock := &mockLLMScorer{
		queue: []mockResult{
			{&LLMScoreResponse{
				Dimensions: LLMDimensions{Skills: 65, Seniority: 60, Domain: 40, Title: 55},
				Gaps:       []string{"no ML experience", "no deep learning background"},
				Strengths:  []string{"strong infrastructure background"},
				Rationale:  "domain mismatch: ML vs infrastructure",
			}, nil},
		},
	}
	s := &Scorer{LLM: mock}
	result, err := s.ScoreMatch(context.Background(), mlJD(), validResume())
	if err != nil {
		t.Fatalf("ScoreMatch() error = %v", err)
	}
	if result.Dimensions.Domain > 50 {
		t.Errorf("Domain = %d, want ≤ 50 for ML JD vs infra resume", result.Dimensions.Domain)
	}
}

// --- BT-4: Gaps non-empty when OverallScore < 85 ---

// BT-4: MatchResult.Gaps has ≥1 item when OverallScore < 85.
// Fixture scores: 70*0.4+60*0.25+65*0.25+50*0.1 = 64 (well below 85).
func TestScoreMatch_BT4_GapsNonEmptyWhenScoreBelow85(t *testing.T) {
	mock := &mockLLMScorer{
		queue: []mockResult{
			{validLLMResponse(70, 60, 65, 50), nil},
		},
	}
	s := &Scorer{LLM: mock}
	result, err := s.ScoreMatch(context.Background(), validJD(), validResume())
	if err != nil {
		t.Fatalf("ScoreMatch() error = %v", err)
	}
	if result.OverallScore >= 85 {
		t.Fatalf("precondition failed: OverallScore = %d, test requires < 85", result.OverallScore)
	}
	if len(result.Gaps) == 0 {
		t.Errorf("Gaps is empty, want ≥1 item when OverallScore = %d", result.OverallScore)
	}
}

// --- BT-5: VerdictReason non-empty on all verdicts ---

// BT-5: All four verdict values produce a non-empty VerdictReason.
func TestScoreMatch_BT5_VerdictReasonNonEmptyOnAllVerdicts(t *testing.T) {
	// Scores chosen to land cleanly in each verdict band:
	//   strong_fit  (>85):  95*0.4+90*0.25+90*0.25+85*0.1 = 38+22.5+22.5+8.5 = 91.5 → 92
	//   apply     (40–85):  70*0.4+60*0.25+65*0.25+50*0.1 = 28+15+16.25+5   = 64.25 → 64
	//   weak_fit  (25–39):  30*0.4+25*0.25+35*0.25+30*0.1 = 12+6.25+8.75+3  = 30
	//   do_not_apply (<25): 15*0.4+15*0.25+15*0.25+15*0.1 = 6+3.75+3.75+1.5 = 15
	cases := []struct {
		name   string
		dims   [4]int // skills, seniority, domain, title
		want   Verdict
	}{
		{"strong_fit", [4]int{95, 90, 90, 85}, VerdictStrongFit},
		{"apply", [4]int{70, 60, 65, 50}, VerdictApply},
		{"weak_fit", [4]int{30, 25, 35, 30}, VerdictWeakFit},
		{"do_not_apply", [4]int{15, 15, 15, 15}, VerdictDoNotApply},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			resp := &LLMScoreResponse{
				Dimensions: LLMDimensions{
					Skills:    tt.dims[0],
					Seniority: tt.dims[1],
					Domain:    tt.dims[2],
					Title:     tt.dims[3],
				},
				Gaps:      []string{"gap1"},
				Strengths: []string{"str1"},
				Rationale: "rationale for " + tt.name,
			}
			mock := &mockLLMScorer{queue: []mockResult{{resp, nil}}}
			s := &Scorer{LLM: mock}
			result, err := s.ScoreMatch(context.Background(), validJD(), validResume())
			if err != nil {
				t.Fatalf("ScoreMatch() error = %v", err)
			}
			if result.Verdict != tt.want {
				t.Errorf("Verdict = %q, want %q (OverallScore=%d)", result.Verdict, tt.want, result.OverallScore)
			}
			if result.VerdictReason == "" {
				t.Errorf("VerdictReason is empty for verdict %q", result.Verdict)
			}
		})
	}
}

// --- Observability: score.completed log event ---

// OBS-1: score.completed is logged on every successful ScoreMatch call,
// carrying overall_score, verdict, latency_ms, and attempt fields.
func TestScoreMatch_Obs_LogsCompleted(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	mock := &mockLLMScorer{queue: []mockResult{{validLLMResponse(80, 70, 75, 60), nil}}}
	s := &Scorer{LLM: mock, Logger: logger}

	result, err := s.ScoreMatch(context.Background(), validJD(), validResume())
	if err != nil {
		t.Fatalf("ScoreMatch() error = %v", err)
	}
	out := buf.String()
	for _, want := range []string{"score.completed", "overall_score", "verdict", "latency_ms", "attempt"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %q\ngot: %s", want, out)
		}
	}
	_ = result
}

// OBS-2: score.retry is logged at WARN when the first LLM call returns ErrScoreInvalidResponse.
func TestScoreMatch_Obs_LogsRetry(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	mock := &mockLLMScorer{queue: []mockResult{
		{nil, ErrScoreInvalidResponse},
		{validLLMResponse(80, 70, 75, 60), nil},
	}}
	s := &Scorer{LLM: mock, Logger: logger}

	_, err := s.ScoreMatch(context.Background(), validJD(), validResume())
	if err != nil {
		t.Fatalf("ScoreMatch() error = %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "score.retry") {
		t.Errorf("expected score.retry log event\ngot: %s", out)
	}
}

// OBS-3: score.failed is logged at ERROR when ScoreMatch returns an error.
func TestScoreMatch_Obs_LogsFailed(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	mock := &mockLLMScorer{queue: []mockResult{{nil, ErrScoreLLMTimeout}}}
	s := &Scorer{LLM: mock, Logger: logger}

	_, err := s.ScoreMatch(context.Background(), validJD(), validResume())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	out := buf.String()
	if !strings.Contains(out, "score.failed") {
		t.Errorf("expected score.failed log event\ngot: %s", out)
	}
}

// OBS-4: MetricsRecorder receives RecordScoreLatency + RecordVerdict on success.
func TestScoreMatch_Obs_MetricsOnSuccess(t *testing.T) {
	mc := &captureMetrics{}
	mock := &mockLLMScorer{queue: []mockResult{{validLLMResponse(80, 70, 75, 60), nil}}}
	s := &Scorer{LLM: mock, Metrics: mc}

	result, err := s.ScoreMatch(context.Background(), validJD(), validResume())
	if err != nil {
		t.Fatalf("ScoreMatch() error = %v", err)
	}
	mc.mu.Lock()
	defer mc.mu.Unlock()
	if len(mc.latencies) != 1 {
		t.Errorf("RecordScoreLatency called %d times, want 1", len(mc.latencies))
	}
	if len(mc.verdicts) != 1 || mc.verdicts[0] != string(result.Verdict) {
		t.Errorf("RecordVerdict = %v, want [%q]", mc.verdicts, result.Verdict)
	}
}

// OBS-5: MetricsRecorder receives RecordRetry + RecordScoreLatency on retry-then-success.
func TestScoreMatch_Obs_MetricsOnRetry(t *testing.T) {
	mc := &captureMetrics{}
	mock := &mockLLMScorer{queue: []mockResult{
		{nil, ErrScoreInvalidResponse},
		{validLLMResponse(80, 70, 75, 60), nil},
	}}
	s := &Scorer{LLM: mock, Metrics: mc}

	_, err := s.ScoreMatch(context.Background(), validJD(), validResume())
	if err != nil {
		t.Fatalf("ScoreMatch() error = %v", err)
	}
	mc.mu.Lock()
	defer mc.mu.Unlock()
	if mc.retries != 1 {
		t.Errorf("RecordRetry called %d times, want 1", mc.retries)
	}
	if len(mc.latencies) != 1 || mc.latencies[0].attempt != 2 {
		t.Errorf("RecordScoreLatency attempt = %v, want attempt=2", mc.latencies)
	}
}

// OBS-6: MetricsRecorder receives RecordScoreError on timeout.
func TestScoreMatch_Obs_MetricsOnError(t *testing.T) {
	mc := &captureMetrics{}
	mock := &mockLLMScorer{queue: []mockResult{{nil, ErrScoreLLMTimeout}}}
	s := &Scorer{LLM: mock, Metrics: mc}

	_, err := s.ScoreMatch(context.Background(), validJD(), validResume())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	mc.mu.Lock()
	defer mc.mu.Unlock()
	if len(mc.errors) != 1 || mc.errors[0] != "score/timeout" {
		t.Errorf("RecordScoreError = %v, want [\"score/timeout\"]", mc.errors)
	}
}

// --- BT-6: prompt PII and secret check ---

// BT-6: buildScoringPrompt must not include email addresses, API keys, or secret tokens.
// Tests the prompt builder in isolation — no live LLM call required.
func TestBuildScoringPrompt_BT6_NoPIIOrSecrets(t *testing.T) {
	prompt := buildScoringPrompt(validJD(), validResume())

	// No email addresses
	emailRE := regexp.MustCompile(`\b[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}\b`)
	if m := emailRE.FindString(prompt); m != "" {
		t.Errorf("prompt contains email address: %q", m)
	}

	// No known secret prefixes or credential patterns
	forbidden := []string{"sk-ant-", "Bearer ", "api_key=", "token=", "password=", "secret=", "Authorization:"}
	for _, pat := range forbidden {
		if strings.Contains(prompt, pat) {
			t.Errorf("prompt contains forbidden secret pattern %q", pat)
		}
	}

	// Prompt must be non-empty and contain expected structural sections
	if !strings.Contains(prompt, "Job Description") {
		t.Error("prompt missing Job Description section")
	}
	if !strings.Contains(prompt, "Candidate Resume") {
		t.Error("prompt missing Candidate Resume section")
	}
	if !strings.Contains(prompt, "Output Format") {
		t.Error("prompt missing Output Format section")
	}
}

// --- parseScoringResponse unit tests ---

func TestParseScoringResponse_CleanJSON(t *testing.T) {
	raw := `{"dimensions":{"skills":80,"seniority":70,"domain":75,"title":60},"gaps":["g1"],"strengths":["s1"],"rationale":"good match"}`
	resp, err := parseScoringResponse([]byte(raw))
	if err != nil {
		t.Fatalf("parseScoringResponse() error = %v", err)
	}
	if resp.Dimensions.Skills != 80 {
		t.Errorf("Skills = %d, want 80", resp.Dimensions.Skills)
	}
	if resp.Rationale != "good match" {
		t.Errorf("Rationale = %q, want \"good match\"", resp.Rationale)
	}
}

func TestParseScoringResponse_MarkdownFence(t *testing.T) {
	raw := "```json\n{\"dimensions\":{\"skills\":75,\"seniority\":65,\"domain\":70,\"title\":55},\"gaps\":[\"g1\"],\"strengths\":[\"s1\"],\"rationale\":\"ok\"}\n```"
	resp, err := parseScoringResponse([]byte(raw))
	if err != nil {
		t.Fatalf("parseScoringResponse() error = %v", err)
	}
	if resp.Dimensions.Skills != 75 {
		t.Errorf("Skills = %d, want 75 (markdown fence stripped)", resp.Dimensions.Skills)
	}
}

func TestParseScoringResponse_MissingRationale(t *testing.T) {
	raw := `{"dimensions":{"skills":80,"seniority":70,"domain":75,"title":60},"gaps":["g1"],"strengths":["s1"],"rationale":""}`
	_, err := parseScoringResponse([]byte(raw))
	if err == nil {
		t.Error("expected error for missing rationale, got nil")
	}
}

func TestParseScoringResponse_NoJSON(t *testing.T) {
	_, err := parseScoringResponse([]byte("I cannot score this resume."))
	if err == nil {
		t.Error("expected error when no JSON in response, got nil")
	}
}

// --- captureMetrics ---

type captureMetrics struct {
	mu        sync.Mutex
	latencies []struct {
		verdict string
		attempt int
		ms      int64
	}
	errors   []string
	verdicts []string
	retries  int
}

func (c *captureMetrics) RecordScoreLatency(verdict string, attempt int, latencyMs int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.latencies = append(c.latencies, struct {
		verdict string
		attempt int
		ms      int64
	}{verdict, attempt, latencyMs})
}

func (c *captureMetrics) RecordScoreError(errorClass string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errors = append(c.errors, errorClass)
}

func (c *captureMetrics) RecordVerdict(verdict string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.verdicts = append(c.verdicts, verdict)
}

func (c *captureMetrics) RecordRetry() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.retries++
}
