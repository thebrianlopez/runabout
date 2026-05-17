package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/thebrianlopez/runabout/cmd/runway/internal/scoring"
)

// --- mock HTTP ---

type mockRoundTripper struct {
	responses []*http.Response
	errors    []error
	calls     int
}

func (m *mockRoundTripper) Do(req *http.Request) (*http.Response, error) {
	if m.calls >= len(m.responses) {
		panic(fmt.Sprintf("mockRoundTripper: unexpected call %d to %s", m.calls, req.URL))
	}
	resp, err := m.responses[m.calls], m.errors[m.calls]
	m.calls++
	return resp, err
}

func jsonBody(v any) io.ReadCloser {
	b, _ := json.Marshal(v)
	return io.NopCloser(strings.NewReader(string(b)))
}

func httpResp(code int, body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: code, Body: body, Header: make(http.Header)}
}

// --- mock LLM extractor ---

type mockJDExtractor struct {
	jd  *scoring.JobDescription
	err error
}

func (m *mockJDExtractor) ExtractJD(_ context.Context, _ string) (*scoring.JobDescription, error) {
	return m.jd, m.err
}

func validExtractedJD() *scoring.JobDescription {
	return &scoring.JobDescription{
		Title:          "Staff Infrastructure Engineer",
		Company:        "Stripe",
		RequiredSkills: []string{"Terraform", "Kubernetes", "Go"},
		SeniorityLevel: "staff",
		Domain:         []string{"infrastructure", "platform"},
	}
}

// --- parseGreenhouseURL ---

func TestParseGreenhouseURL_Valid(t *testing.T) {
	token, id, err := parseGreenhouseURL("https://boards.greenhouse.io/stripe/jobs/6474506003")
	if err != nil {
		t.Fatalf("parseGreenhouseURL() error = %v", err)
	}
	if token != "stripe" {
		t.Errorf("boardToken = %q, want \"stripe\"", token)
	}
	if id != "6474506003" {
		t.Errorf("jobID = %q, want \"6474506003\"", id)
	}
}

func TestParseGreenhouseURL_Invalid(t *testing.T) {
	cases := []string{
		"https://example.com/jobs/123",
		"not-a-url",
		"https://boards.greenhouse.io/stripe",         // missing /jobs/ID
		"https://boards.greenhouse.io/stripe/jobs/abc", // non-numeric ID
	}
	for _, url := range cases {
		_, _, err := parseGreenhouseURL(url)
		if err == nil {
			t.Errorf("parseGreenhouseURL(%q) = nil error, want error", url)
		}
	}
}

// --- CT: JobDescription JSON schema roundtrip ---

func TestJobDescription_JSONRoundtrip(t *testing.T) {
	original := validExtractedJD()
	original.SourceURL = "https://boards.greenhouse.io/stripe/jobs/6474506003"
	original.RawText = "We are hiring a staff engineer..."

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}
	var got scoring.JobDescription
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}
	if got.Title != original.Title {
		t.Errorf("Title = %q, want %q", got.Title, original.Title)
	}
	if len(got.RequiredSkills) != len(original.RequiredSkills) {
		t.Errorf("RequiredSkills len = %d, want %d", len(got.RequiredSkills), len(original.RequiredSkills))
	}
	if got.SeniorityLevel != original.SeniorityLevel {
		t.Errorf("SeniorityLevel = %q, want %q", got.SeniorityLevel, original.SeniorityLevel)
	}
}

// --- ERR_JD_NOT_FOUND ---

func TestIngestJD_ErrJDNotFound(t *testing.T) {
	mock := &mockRoundTripper{
		responses: []*http.Response{
			httpResp(http.StatusOK, jsonBody(greenhouseBoardResponse{Name: "Stripe"})),
			httpResp(http.StatusNotFound, io.NopCloser(strings.NewReader(""))),
		},
		errors: []error{nil, nil},
	}
	ing := &Ingester{HTTP: mock, LLM: &mockJDExtractor{jd: validExtractedJD()}, GreenhouseAPIBase: "http://test"}
	_, err := ing.IngestJD(context.Background(), IngestSource{URL: "https://boards.greenhouse.io/stripe/jobs/9999"})

	var ingestErr *IngestError
	if !errors.As(err, &ingestErr) || ingestErr.Code != "ingest/not_found" {
		t.Errorf("error = %v (%T), want ingest/not_found", err, err)
	}
}

// --- ERR_GREENHOUSE_RATE_LIMIT ---

func TestIngestJD_ErrGreenhouseRateLimit(t *testing.T) {
	rateLimitResp := httpResp(http.StatusTooManyRequests, io.NopCloser(strings.NewReader("")))
	rateLimitResp.Header.Set("Retry-After", "30")

	mock := &mockRoundTripper{
		responses: []*http.Response{
			httpResp(http.StatusOK, jsonBody(greenhouseBoardResponse{Name: "Stripe"})),
			rateLimitResp,
		},
		errors: []error{nil, nil},
	}
	ing := &Ingester{HTTP: mock, LLM: &mockJDExtractor{jd: validExtractedJD()}, GreenhouseAPIBase: "http://test"}
	_, err := ing.IngestJD(context.Background(), IngestSource{URL: "https://boards.greenhouse.io/stripe/jobs/123"})

	var ingestErr *IngestError
	if !errors.As(err, &ingestErr) || ingestErr.Code != "ingest/rate_limit" {
		t.Errorf("error = %v, want ingest/rate_limit", err)
	}
	if ingestErr.RetryAfter != 30 {
		t.Errorf("RetryAfter = %d, want 30", ingestErr.RetryAfter)
	}
}

// --- ERR_JD_PARSE_FAILED ---

func TestIngestJD_ErrJDParseFailed_LLMError(t *testing.T) {
	mock := &mockRoundTripper{
		responses: []*http.Response{
			httpResp(http.StatusOK, jsonBody(greenhouseBoardResponse{Name: "Stripe"})),
			httpResp(http.StatusOK, jsonBody(greenhouseJobResponse{ID: 1, Title: "SRE", Content: "<p>some content</p>"})),
		},
		errors: []error{nil, nil},
	}
	ing := &Ingester{
		HTTP:              mock,
		LLM:               &mockJDExtractor{err: ErrJDParseFailed},
		GreenhouseAPIBase: "http://test",
	}
	_, err := ing.IngestJD(context.Background(), IngestSource{URL: "https://boards.greenhouse.io/stripe/jobs/1"})

	var ingestErr *IngestError
	if !errors.As(err, &ingestErr) || ingestErr.Code != "ingest/parse_failed" {
		t.Errorf("error = %v, want ingest/parse_failed", err)
	}
}

// --- ERR_JD_TOO_LONG ---

func TestIngestJD_ErrJDTooLong_TextPath(t *testing.T) {
	tooLong := strings.Repeat("word ", maxWords+1)
	ing := &Ingester{LLM: &mockJDExtractor{jd: validExtractedJD()}}
	_, err := ing.IngestJD(context.Background(), IngestSource{Text: tooLong})

	var ingestErr *IngestError
	if !errors.As(err, &ingestErr) || ingestErr.Code != "ingest/too_long" {
		t.Errorf("error = %v, want ingest/too_long", err)
	}
}

func TestIngestJD_ErrJDTooLong_URLPath(t *testing.T) {
	longContent := strings.Repeat("word ", maxWords+1)
	mock := &mockRoundTripper{
		responses: []*http.Response{
			httpResp(http.StatusOK, jsonBody(greenhouseBoardResponse{Name: "Co"})),
			httpResp(http.StatusOK, jsonBody(greenhouseJobResponse{ID: 1, Title: "SRE", Content: longContent})),
		},
		errors: []error{nil, nil},
	}
	ing := &Ingester{HTTP: mock, LLM: &mockJDExtractor{jd: validExtractedJD()}, GreenhouseAPIBase: "http://test"}
	_, err := ing.IngestJD(context.Background(), IngestSource{URL: "https://boards.greenhouse.io/co/jobs/1"})

	var ingestErr *IngestError
	if !errors.As(err, &ingestErr) || ingestErr.Code != "ingest/too_long" {
		t.Errorf("error = %v, want ingest/too_long", err)
	}
}

// --- happy paths ---

func TestIngestJD_URLPath_PopulatesAllFields(t *testing.T) {
	mock := &mockRoundTripper{
		responses: []*http.Response{
			httpResp(http.StatusOK, jsonBody(greenhouseBoardResponse{Name: "Stripe"})),
			httpResp(http.StatusOK, jsonBody(greenhouseJobResponse{
				ID:      6474506003,
				Title:   "Staff Infrastructure Engineer",
				Content: "<p>We need Terraform and Kubernetes skills.</p>",
			})),
		},
		errors: []error{nil, nil},
	}
	extracted := validExtractedJD()
	ing := &Ingester{HTTP: mock, LLM: &mockJDExtractor{jd: extracted}, GreenhouseAPIBase: "http://test"}

	jd, err := ing.IngestJD(context.Background(), IngestSource{URL: "https://boards.greenhouse.io/stripe/jobs/6474506003"})
	if err != nil {
		t.Fatalf("IngestJD() error = %v", err)
	}
	if jd.Title != "Staff Infrastructure Engineer" {
		t.Errorf("Title = %q, want structured API title", jd.Title)
	}
	if jd.Company != "Stripe" {
		t.Errorf("Company = %q, want \"Stripe\"", jd.Company)
	}
	if jd.SourceURL == "" {
		t.Error("SourceURL is empty")
	}
	if jd.RawText == "" {
		t.Error("RawText is empty")
	}
	if len(jd.RequiredSkills) == 0 {
		t.Error("RequiredSkills is empty")
	}
}

func TestIngestJD_TextPath_PopulatesRequiredSkills(t *testing.T) {
	text := "We are looking for a staff-level engineer with Terraform, Kubernetes, and Go experience."
	ing := &Ingester{LLM: &mockJDExtractor{jd: validExtractedJD()}}

	jd, err := ing.IngestJD(context.Background(), IngestSource{Text: text})
	if err != nil {
		t.Fatalf("IngestJD() error = %v", err)
	}
	if len(jd.RequiredSkills) == 0 {
		t.Error("RequiredSkills is empty for text path")
	}
	if jd.RawText != text {
		t.Errorf("RawText = %q, want original text", jd.RawText)
	}
}

// --- parseExtractionResponse ---

func TestParseExtractionResponse_Valid(t *testing.T) {
	raw := `{"title":"SRE","company":"Acme","required_skills":["Go","k8s"],"preferred_skills":[],"seniority_level":"senior","domain":["infrastructure"]}`
	jd, err := parseExtractionResponse([]byte(raw))
	if err != nil {
		t.Fatalf("parseExtractionResponse() error = %v", err)
	}
	if jd.Title != "SRE" {
		t.Errorf("Title = %q, want \"SRE\"", jd.Title)
	}
	if len(jd.RequiredSkills) != 2 {
		t.Errorf("RequiredSkills = %v, want 2 items", jd.RequiredSkills)
	}
}

func TestParseExtractionResponse_EmptySkillsRejected(t *testing.T) {
	raw := `{"title":"SRE","company":"Acme","required_skills":[],"seniority_level":"senior","domain":[]}`
	_, err := parseExtractionResponse([]byte(raw))
	if err == nil {
		t.Error("expected error for empty required_skills, got nil")
	}
}

func TestParseExtractionResponse_MarkdownFence(t *testing.T) {
	raw := "```json\n{\"title\":\"SRE\",\"company\":\"Acme\",\"required_skills\":[\"Go\"],\"seniority_level\":\"senior\",\"domain\":[]}\n```"
	jd, err := parseExtractionResponse([]byte(raw))
	if err != nil {
		t.Fatalf("parseExtractionResponse() error = %v (markdown fence not stripped)", err)
	}
	if jd.Title != "SRE" {
		t.Errorf("Title = %q, want \"SRE\"", jd.Title)
	}
}

// --- stripHTML ---

func TestStripHTML(t *testing.T) {
	cases := []struct {
		html string
		want string
	}{
		{"<p>Hello <b>world</b></p>", "Hello world"},
		{"<div>A &amp; B</div>", "A & B"},
		{"<br/>  \n  <p>text</p>", "text"},
	}
	for _, tt := range cases {
		got := stripHTML(tt.html)
		if got != tt.want {
			t.Errorf("stripHTML(%q) = %q, want %q", tt.html, got, tt.want)
		}
	}
}

// --- F1→F2 pipeline integration ---

// TestPipeline_IngestThenScore validates the full F1→F2 pipeline using mocks.
// IngestJD produces a JobDescription; ScoreMatch consumes it and returns a MatchResult.
func TestPipeline_IngestThenScore(t *testing.T) {
	// F1: ingest via text path with mock extractor
	jdText := "Looking for a staff infrastructure engineer with Terraform, Kubernetes, and AWS."
	ing := &Ingester{LLM: &mockJDExtractor{jd: &scoring.JobDescription{
		Title:          "Staff Infrastructure Engineer",
		Company:        "Stripe",
		RequiredSkills: []string{"Terraform", "Kubernetes", "AWS"},
		SeniorityLevel: "staff",
	}}}
	jd, err := ing.IngestJD(context.Background(), IngestSource{Text: jdText})
	if err != nil {
		t.Fatalf("IngestJD() error = %v", err)
	}

	// F2: score with mock LLM scorer
	resume := &scoring.Resume{
		Summary: "15+ years infrastructure engineer",
		Skills:  map[string][]string{"Infrastructure": {"Terraform", "AWS", "Kubernetes"}},
		Experience: []scoring.ExperienceEntry{
			{Company: "Grindr", Roles: []scoring.Role{{Title: "Senior Cloud Engineer", Bullets: []string{"Managed EKS clusters"}}}},
		},
	}
	mockScorer := &mockLLMScorer{
		resp: &scoring.LLMScoreResponse{
			Dimensions: scoring.LLMDimensions{Skills: 90, Seniority: 75, Domain: 80, Title: 70},
			Gaps:       []string{"gap1"},
			Strengths:  []string{"str1"},
			Rationale:  "strong infra match",
		},
	}
	scorer := &scoring.Scorer{LLM: mockScorer}
	result, err := scorer.ScoreMatch(context.Background(), jd, resume)
	if err != nil {
		t.Fatalf("ScoreMatch() error = %v", err)
	}
	if result.OverallScore == 0 {
		t.Error("OverallScore is 0 — pipeline did not compute score")
	}
	if result.Verdict == "" {
		t.Error("Verdict is empty")
	}
}

// mockLLMScorer is the inline mock for the pipeline test.
type mockLLMScorer struct {
	resp *scoring.LLMScoreResponse
	err  error
}

func (m *mockLLMScorer) ScoreWithLLM(_ context.Context, _ *scoring.JobDescription, _ *scoring.Resume) (*scoring.LLMScoreResponse, error) {
	return m.resp, m.err
}
