package serving

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- mocks ---

type mockRender struct {
	err       error
	callCount int
}

// Render creates stub format files so uploadAll can read them.
func (m *mockRender) Render(_ context.Context, slug, _, outputDir string) error {
	m.callCount++
	if m.err != nil {
		return m.err
	}
	for _, v := range allVariants {
		name := fmt.Sprintf("%s/resume-%s%s", outputDir, slug, v.ext)
		if err := os.WriteFile(name, []byte("stub-"+v.name), 0o600); err != nil {
			return err
		}
	}
	return nil
}

type s3Call struct {
	bucket string
	key    string
	body   []byte
}

type mockS3 struct {
	mu      sync.Mutex
	calls   []s3Call
	errors  []error // per call; loops back to last if exhausted
	callIdx int
}

func (m *mockS3) PutObject(_ context.Context, bucket, key string, body io.Reader) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, _ := io.ReadAll(body)
	m.calls = append(m.calls, s3Call{bucket: bucket, key: key, body: data})
	idx := m.callIdx
	m.callIdx++
	if len(m.errors) == 0 {
		return nil
	}
	if idx >= len(m.errors) {
		return m.errors[len(m.errors)-1]
	}
	return m.errors[idx]
}

func (m *mockS3) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

type ssmCall struct {
	name  string
	value string
}

type mockSSM struct {
	mu    sync.Mutex
	calls []ssmCall
	err   error
}

func (m *mockSSM) PutParameter(_ context.Context, name, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, ssmCall{name: name, value: value})
	return m.err
}

func (m *mockSSM) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// newStorer builds a Storer with no-delay retries (zero RetryDelays).
func newStorer(render RenderPipeline, s3 S3Uploader, ssm SSMWriter) *Storer {
	return &Storer{
		Render:      render,
		S3:          s3,
		SSM:         ssm,
		RetryDelays: []time.Duration{0, 0, 0},
		Now:         func() time.Time { return time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC) },
	}
}

const testSlug = "stripe-staff-engineer-20260508"

// --- CT-1: all 6 S3 keys uploaded on success ---

// CT-1: ServeVariant calls PutObject exactly 6 times with correct key patterns on success.
func TestServeVariant_CT1_AllSixKeysUploaded(t *testing.T) {
	s3 := &mockS3{}
	st := newStorer(&mockRender{}, s3, &mockSSM{})

	_, err := st.ServeVariant(context.Background(), testSlug, "", false)
	if err != nil {
		t.Fatalf("ServeVariant() error = %v", err)
	}
	if s3.callCount() != 6 {
		t.Errorf("PutObject call count = %d, want 6", s3.callCount())
	}

	wantKeys := []string{
		"lambda/resume-" + testSlug,
		"lambda/resume-" + testSlug + ".pdf",
		"lambda/resume-" + testSlug + ".json",
		"lambda/resume-" + testSlug + ".md",
		"lambda/resume-" + testSlug + ".txt",
		"lambda/resume-" + testSlug + ".yaml",
	}
	s3.mu.Lock()
	defer s3.mu.Unlock()
	for i, want := range wantKeys {
		if s3.calls[i].key != want {
			t.Errorf("call[%d].key = %q, want %q", i, s3.calls[i].key, want)
		}
	}
}

// --- CT-2: S3 key pattern matches Lambda@Edge routing regex ---

// CT-2: All 6 S3 keys match the Lambda@Edge routing pattern lambda/resume-[a-z0-9-]+(\.[a-z]+)?
func TestServeVariant_CT2_S3KeyPatternMatchesLambdaEdgeRegex(t *testing.T) {
	lambdaRE := regexp.MustCompile(`^lambda/resume-[a-z0-9-]+(\.[a-z]+)?$`)
	s3Prefix := "lambda/resume-" + testSlug
	for _, v := range allVariants {
		key := s3Prefix + v.ext
		if !lambdaRE.MatchString(key) {
			t.Errorf("key %q does not match Lambda@Edge routing regex", key)
		}
	}
}

// --- CT-3: SSM parameter schema correctness ---

// CT-3: SSM value contains created_at, expires_at, s3_prefix; expires_at is 7 days after created_at.
func TestServeVariant_CT3_SSMParameterSchema(t *testing.T) {
	ssm := &mockSSM{}
	st := newStorer(&mockRender{}, &mockS3{}, ssm)

	_, err := st.ServeVariant(context.Background(), testSlug, "", false)
	if err != nil {
		t.Fatalf("ServeVariant() error = %v", err)
	}
	if ssm.callCount() != 1 {
		t.Fatalf("SSM PutParameter call count = %d, want 1", ssm.callCount())
	}

	ssm.mu.Lock()
	call := ssm.calls[0]
	ssm.mu.Unlock()

	if !strings.Contains(call.name, testSlug) {
		t.Errorf("SSM param name %q does not contain slug", call.name)
	}
	if !strings.Contains(call.value, "created_at") {
		t.Errorf("SSM value missing created_at field: %s", call.value)
	}
	if !strings.Contains(call.value, "expires_at") {
		t.Errorf("SSM value missing expires_at field: %s", call.value)
	}
	if !strings.Contains(call.value, "s3_prefix") {
		t.Errorf("SSM value missing s3_prefix field: %s", call.value)
	}
	// expires_at should be 7 days after created_at
	if !strings.Contains(call.value, "2026-05-15") {
		t.Errorf("SSM expires_at should be 2026-05-15 (7 days after 2026-05-08), got: %s", call.value)
	}
}

// --- CT-4: S3 retry on transient failure ---

// CT-4: Given first 2 PutObject calls fail, third succeeds — exactly 3 total calls, no error.
func TestServeVariant_CT4_S3RetryOnTransientFailure(t *testing.T) {
	transient := errors.New("connection reset")
	// First 2 calls per format fail; 3rd succeeds.
	// But since allVariants has 6 formats × 3 attempts = up to 18 calls,
	// use a counter-based mock to fail the first 2 calls of the FIRST format only.
	s3 := &mockS3{
		errors: []error{transient, transient, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil},
	}
	st := newStorer(&mockRender{}, s3, &mockSSM{})

	_, err := st.ServeVariant(context.Background(), testSlug, "", false)
	if err != nil {
		t.Fatalf("ServeVariant() error = %v, want nil (retry should succeed)", err)
	}

	// First format: 3 calls (2 fail + 1 succeed); remaining 5 formats: 1 call each = 8 total
	if s3.callCount() != 8 {
		t.Errorf("PutObject call count = %d, want 8 (3 for first format + 1 each for 5 others)", s3.callCount())
	}
}

// --- CT-5: S3 fatal after 3 failures ---

// CT-5: Given all PutObject calls fail, ERR_S3_UPLOAD_FAILED returned; no SSM write attempted.
func TestServeVariant_CT5_S3FatalAfterThreeFailures(t *testing.T) {
	persistent := errors.New("access denied")
	s3 := &mockS3{errors: []error{persistent}}
	ssm := &mockSSM{}
	st := newStorer(&mockRender{}, s3, ssm)

	_, err := st.ServeVariant(context.Background(), testSlug, "", false)
	var serveErr *ServeError
	if !errors.As(err, &serveErr) || serveErr.Code != "serve/s3_upload_failed" {
		t.Errorf("error = %v, want serve/s3_upload_failed", err)
	}
	if s3.callCount() != 3 {
		t.Errorf("PutObject call count = %d, want 3 (maxUploadRetries for first format)", s3.callCount())
	}
	if ssm.callCount() != 0 {
		t.Errorf("SSM PutParameter called %d times after S3 failure, want 0", ssm.callCount())
	}
}

// --- CT-6: SSM failure is warning not fatal ---

// CT-6: Given SSM PutParameter fails, ServeResult.SSMWritten = false returned; no error.
func TestServeVariant_CT6_SSMFailureIsWarning(t *testing.T) {
	ssm := &mockSSM{err: errors.New("access denied")}
	st := newStorer(&mockRender{}, &mockS3{}, ssm)

	result, err := st.ServeVariant(context.Background(), testSlug, "", false)
	if err != nil {
		t.Fatalf("ServeVariant() error = %v, want nil (SSM failure is not fatal)", err)
	}
	if result.SSMWritten {
		t.Error("SSMWritten = true, want false when SSM write failed")
	}
}

// --- CT-7: ERR_RENDER_FAILED on non-zero render exit ---

// CT-7: Given Render returns error, ERR_RENDER_FAILED returned; no S3 upload attempted (RG-1).
func TestServeVariant_CT7_RenderFailureIsFatal(t *testing.T) {
	render := &mockRender{err: errors.New("typst: command not found")}
	s3 := &mockS3{}
	st := newStorer(render, s3, &mockSSM{})

	_, err := st.ServeVariant(context.Background(), testSlug, "", false)
	var serveErr *ServeError
	if !errors.As(err, &serveErr) || serveErr.Code != "serve/render_failed" {
		t.Errorf("error = %v, want serve/render_failed", err)
	}
	if s3.callCount() != 0 {
		t.Errorf("PutObject call count = %d, want 0 (no upload after render failure)", s3.callCount())
	}
}

// --- CT-8: slug validation ---

// CT-8: Given slug with uppercase or special chars, ERR_SLUG_INVALID returned before render.
func TestServeVariant_CT8_SlugValidation(t *testing.T) {
	render := &mockRender{}
	cases := []string{
		"Stripe-staff-20260508", // uppercase
		"stripe_staff_20260508", // underscore
		"stripe/staff/20260508", // slash (path traversal)
		"stripe staff 20260508", // space
		"",                      // empty
		"../etc/passwd",         // traversal attempt
	}
	for _, slug := range cases {
		st := newStorer(render, &mockS3{}, &mockSSM{})
		_, err := st.ServeVariant(context.Background(), slug, "", false)
		var serveErr *ServeError
		if !errors.As(err, &serveErr) || serveErr.Code != "serve/slug_invalid" {
			t.Errorf("slug %q: error = %v, want serve/slug_invalid", slug, err)
		}
	}
	if render.callCount != 0 {
		t.Errorf("Render called %d times, want 0 (slug validation must fire before render)", render.callCount)
	}
}

// --- CT-9: UsedFallback resume served correctly ---

// CT-9: ServeVariant with usedFallback=true completes successfully — fallback YAML is
// treated identically to tailored YAML by the serving layer.
func TestServeVariant_CT9_FallbackResumeReachesS3(t *testing.T) {
	s3 := &mockS3{}
	st := newStorer(&mockRender{}, s3, &mockSSM{})

	result, err := st.ServeVariant(context.Background(), testSlug, "/original/resume.yaml", true)
	if err != nil {
		t.Fatalf("ServeVariant() error = %v", err)
	}
	if s3.callCount() != 6 {
		t.Errorf("PutObject call count = %d, want 6 even for fallback resume", s3.callCount())
	}
	if result.Slug != testSlug {
		t.Errorf("Slug = %q, want %q", result.Slug, testSlug)
	}
}

// --- BT-1: serving URL format correct ---

// BT-1: ServeResult.URL == "https://brianlopez.us/resume/{slug}".
func TestServeVariant_BT1_ServingURLFormat(t *testing.T) {
	st := newStorer(&mockRender{}, &mockS3{}, &mockSSM{})

	result, err := st.ServeVariant(context.Background(), testSlug, "", false)
	if err != nil {
		t.Fatalf("ServeVariant() error = %v", err)
	}
	want := "https://brianlopez.us/resume/" + testSlug
	if result.URL != want {
		t.Errorf("URL = %q, want %q", result.URL, want)
	}
}

// --- BT-2: ExpiresAt is 7 days from creation ---

// BT-2: ServeResult.ExpiresAt is within [6.99, 7.01] days of creation time.
func TestServeVariant_BT2_ExpiresAtSevenDays(t *testing.T) {
	fixedNow := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	st := newStorer(&mockRender{}, &mockS3{}, &mockSSM{})
	st.Now = func() time.Time { return fixedNow }

	result, err := st.ServeVariant(context.Background(), testSlug, "", false)
	if err != nil {
		t.Fatalf("ServeVariant() error = %v", err)
	}
	diff := result.ExpiresAt.Sub(fixedNow)
	minTTL := 7*24*time.Hour - time.Minute
	maxTTL := 7*24*time.Hour + time.Minute
	if diff < minTTL || diff > maxTTL {
		t.Errorf("ExpiresAt - now = %v, want ~7 days", diff)
	}
}

// --- BT-4: S3 uploads are sequential and ordered ---

// BT-4: Uploads occur in format order: html, pdf, json, md, txt, yaml (HTML before PDF, all before SSM).
func TestServeVariant_BT4_UploadsSequentialAndOrdered(t *testing.T) {
	s3 := &mockS3{}
	ssm := &mockSSM{}
	st := newStorer(&mockRender{}, s3, ssm)

	_, err := st.ServeVariant(context.Background(), testSlug, "", false)
	if err != nil {
		t.Fatalf("ServeVariant() error = %v", err)
	}

	s3.mu.Lock()
	keys := make([]string, len(s3.calls))
	for i, c := range s3.calls {
		keys[i] = c.key
	}
	s3.mu.Unlock()

	// HTML must come first
	if len(keys) == 0 || keys[0] != "lambda/resume-"+testSlug {
		t.Errorf("first upload key = %q, want HTML key %q", func() string {
			if len(keys) > 0 {
				return keys[0]
			}
			return "<none>"
		}(), "lambda/resume-"+testSlug)
	}
	// PDF must be second
	if len(keys) < 2 || keys[1] != "lambda/resume-"+testSlug+".pdf" {
		t.Errorf("second upload key = %q, want PDF key", func() string {
			if len(keys) > 1 {
				return keys[1]
			}
			return "<none>"
		}())
	}
	// SSM is called after all 6 S3 uploads
	if s3.callCount() != 6 {
		t.Errorf("only %d S3 uploads before SSM write, want 6", s3.callCount())
	}
}

// --- BT-5: idempotent re-upload same slug ---

// BT-5: Calling ServeVariant twice with the same slug succeeds both times.
func TestServeVariant_BT5_IdempotentReUpload(t *testing.T) {
	st := newStorer(&mockRender{}, &mockS3{}, &mockSSM{})

	_, err := st.ServeVariant(context.Background(), testSlug, "", false)
	if err != nil {
		t.Fatalf("first ServeVariant() error = %v", err)
	}
	_, err = st.ServeVariant(context.Background(), testSlug, "", false)
	if err != nil {
		t.Fatalf("second ServeVariant() (re-upload) error = %v", err)
	}
}

// --- RG-1: S3 upload must not proceed after render failure ---

// RG-1: Given ERR_RENDER_FAILED, PutObject call count == 0.
func TestServeVariant_RG1_NoUploadAfterRenderFailure(t *testing.T) {
	render := &mockRender{err: errors.New("render.py exited 1")}
	s3 := &mockS3{}
	st := newStorer(render, s3, &mockSSM{})

	_, err := st.ServeVariant(context.Background(), testSlug, "", false)
	if err == nil {
		t.Fatal("expected error on render failure")
	}
	if s3.callCount() != 0 {
		t.Errorf("PutObject called %d times after render failure, want 0", s3.callCount())
	}
}

// --- RG-2: SSM write must happen after all 6 S3 uploads ---

// RG-2: SSM PutParameter is called after all 6 successful S3 PutObject calls.
func TestServeVariant_RG2_SSMWriteAfterAllUploads(t *testing.T) {
	ssmCallIdx := -1
	callOrder := 0
	s3 := &mockS3{}
	ssm := &mockSSM{}

	// Intercept: we track that SSM fires after all S3 calls by checking S3 call count
	// at the time SSM is invoked via a custom SSM mock.
	type orderedSSM struct {
		inner      *mockSSM
		s3         *mockS3
		ssmCallIdx *int
	}

	type orderedS3 struct {
		inner    *mockS3
		orderPtr *int
	}

	// Use a sequential tracker: each action logs its sequence index.
	var mu sync.Mutex
	var sequence []string

	trackingS3 := &trackS3{s3: s3, seq: &sequence, mu: &mu}
	trackingSSM := &trackSSM{ssm: ssm, seq: &sequence, mu: &mu}

	st := newStorer(&mockRender{}, trackingS3, trackingSSM)
	_, err := st.ServeVariant(context.Background(), testSlug, "", false)
	if err != nil {
		t.Fatalf("ServeVariant() error = %v", err)
	}

	_ = ssmCallIdx
	_ = callOrder

	mu.Lock()
	defer mu.Unlock()
	if len(sequence) != 7 { // 6 uploads + 1 SSM
		t.Fatalf("sequence length = %d, want 7", len(sequence))
	}
	// Last entry must be SSM
	if sequence[6] != "ssm" {
		t.Errorf("sequence[6] = %q, want \"ssm\" (SSM must be last)", sequence[6])
	}
	// First 6 must all be S3
	for i := 0; i < 6; i++ {
		if sequence[i] != "s3" {
			t.Errorf("sequence[%d] = %q, want \"s3\"", i, sequence[i])
		}
	}
}

// Helpers for RG-2 ordering assertion.

type trackS3 struct {
	s3  *mockS3
	seq *[]string
	mu  *sync.Mutex
}

func (t *trackS3) PutObject(ctx context.Context, bucket, key string, body io.Reader) error {
	t.mu.Lock()
	*t.seq = append(*t.seq, "s3")
	t.mu.Unlock()
	return t.s3.PutObject(ctx, bucket, key, body)
}

type trackSSM struct {
	ssm *mockSSM
	seq *[]string
	mu  *sync.Mutex
}

func (t *trackSSM) PutParameter(ctx context.Context, name, value string) error {
	t.mu.Lock()
	*t.seq = append(*t.seq, "ssm")
	t.mu.Unlock()
	return t.ssm.PutParameter(ctx, name, value)
}

// --- observability ---

func TestServeVariant_Obs_LogsCompleted(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	st := newStorer(&mockRender{}, &mockS3{}, &mockSSM{})
	st.Logger = logger

	_, err := st.ServeVariant(context.Background(), testSlug, "", false)
	if err != nil {
		t.Fatalf("ServeVariant() error = %v", err)
	}
	out := buf.String()
	for _, want := range []string{"serve.completed", "slug", "url"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %q\ngot: %s", want, out)
		}
	}
}

func TestServeVariant_Obs_LogsSSMFailed(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	ssm := &mockSSM{err: errors.New("access denied")}
	st := newStorer(&mockRender{}, &mockS3{}, ssm)
	st.Logger = logger

	_, err := st.ServeVariant(context.Background(), testSlug, "", false)
	if err != nil {
		t.Fatalf("ServeVariant() error = %v, want nil (SSM failure is warning)", err)
	}
	out := buf.String()
	if !strings.Contains(out, "serve.ssm_failed") {
		t.Errorf("expected serve.ssm_failed log event\ngot: %s", out)
	}
}

func TestServeVariant_Obs_LogsRenderFailed(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	render := &mockRender{err: errors.New("typst not found")}
	st := newStorer(render, &mockS3{}, &mockSSM{})
	st.Logger = logger

	_, err := st.ServeVariant(context.Background(), testSlug, "", false)
	if err == nil {
		t.Fatal("expected error on render failure")
	}
	out := buf.String()
	if !strings.Contains(out, "serve.failed") {
		t.Errorf("expected serve.failed log event\ngot: %s", out)
	}
}
