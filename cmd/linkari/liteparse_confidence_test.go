// EPIC-104: Confidence-Aware PDF Extraction via JSON Output — contract + behavioral + regression tests.
// CT-1 through CT-8 are written before implementation (M1 gate).
// BT-1 through BT-3 and RG-1, RG-2 are added in M8.
package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// --- Helpers ---

// fakeLiteCmd returns an execLiteCmd replacement controlled by the caller.
// Each call to the returned func advances an internal counter; responses
// are supplied in order. If more calls are made than responses provided,
// the last response is repeated.
func fakeLiteCmd(responses []struct {
	out []byte
	err error
}, callCount *int,
) func(context.Context, ...string) ([]byte, error) {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		idx := *callCount
		*callCount++
		if idx >= len(responses) {
			idx = len(responses) - 1
		}
		return responses[idx].out, responses[idx].err
	}
}

// installExecLiteCmd overrides execLiteCmd for the duration of the test.
func installExecLiteCmd(t *testing.T, fn func(context.Context, ...string) ([]byte, error)) {
	t.Helper()
	prev := execLiteCmd
	execLiteCmd = fn
	t.Cleanup(func() { execLiteCmd = prev })
}

// --- CT-1: execLiteParse returns confidence from --format json output ---

func TestCT1_JSONParsesConfidence(t *testing.T) {
	// CT-1: parseLiteParseJSON correctly extracts text and confidence from a
	// single-page JSON response.
	in := []byte(`{"pages":[{"text":"hello world","confidence":0.9}]}`)
	text, conf, pageCount, err := parseLiteParseJSON(in)
	if err != nil {
		t.Fatalf("CT-1: unexpected error: %v", err)
	}
	if text != "hello world" {
		t.Errorf("CT-1: text = %q, want %q", text, "hello world")
	}
	if conf < 0.899 || conf > 0.901 {
		t.Errorf("CT-1: confidence = %v, want ~0.9", conf)
	}
	if pageCount != 1 {
		t.Errorf("CT-1: pageCount = %d, want 1", pageCount)
	}
}

// --- CT-2: Confidence < threshold triggers OCR retry ---

func TestCT2_LowConfidenceTriggersOCRRetry(t *testing.T) {
	calls := 0
	installExecLiteCmd(t, fakeLiteCmd([]struct {
		out []byte
		err error
	}{
		{out: []byte(`{"pages":[{"text":"page1","confidence":0.3}]}`), err: nil},     // first: no-OCR, low confidence
		{out: []byte(`{"pages":[{"text":"page1 ocr","confidence":0.8}]}`), err: nil}, // second: OCR retry
	}, &calls))

	text, conf, err := runLiteParse(context.Background(), "fake.pdf", LiteParseConfig{ConfidenceThreshold: 0.5})
	if err != nil {
		t.Fatalf("CT-2: unexpected error: %v", err)
	}
	if calls != 2 {
		t.Errorf("CT-2: execLiteCmd called %d times, want 2 (OCR retry must be triggered)", calls)
	}
	if text != "page1 ocr" {
		t.Errorf("CT-2: text = %q, want %q", text, "page1 ocr")
	}
	if conf < 0.799 || conf > 0.801 {
		t.Errorf("CT-2: confidence = %v, want ~0.8 (post-OCR)", conf)
	}
}

// --- CT-3: Confidence >= threshold skips OCR retry ---

func TestCT3_HighConfidenceSkipsOCRRetry(t *testing.T) {
	// CT-3: high confidence → only ONE lit call is made.
	calls := 0
	installExecLiteCmd(t, fakeLiteCmd([]struct {
		out []byte
		err error
	}{
		{out: []byte(`{"pages":[{"text":"clean text","confidence":0.8}]}`), err: nil},
	}, &calls))

	_, _, err := runLiteParse(context.Background(), "fake.pdf", LiteParseConfig{ConfidenceThreshold: 0.5})
	if err != nil {
		t.Fatalf("CT-3: unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("CT-3: execLiteCmd called %d times, want 1 (no OCR retry when confidence >= threshold)", calls)
	}
}

// --- CT-4: JSON parse failure falls back to plain-text --no-ocr ---

func TestCT4_JSONParseFallbackToPlainText(t *testing.T) {
	calls := 0
	installExecLiteCmd(t, fakeLiteCmd([]struct {
		out []byte
		err error
	}{
		{out: []byte(`this is not json`), err: nil},  // first: --format json returns non-JSON
		{out: []byte(`plain text result`), err: nil}, // second: fallback --no-ocr -q
	}, &calls))

	text, conf, err := runLiteParse(context.Background(), "fake.pdf", LiteParseConfig{ConfidenceThreshold: 0.5})
	if err != nil {
		t.Fatalf("CT-4: unexpected error: %v", err)
	}
	if calls != 2 {
		t.Errorf("CT-4: execLiteCmd called %d times, want 2 (JSON parse failure → fallback call)", calls)
	}
	if text != "plain text result" {
		t.Errorf("CT-4: text = %q, want %q", text, "plain text result")
	}
	if conf != -1.0 {
		t.Errorf("CT-4: confidence = %v, want -1.0 (JSON parse fallback sentinel)", conf)
	}
}

// --- CT-5: Empty pages array → confidence == 0.0 → OCR retry triggered ---

func TestCT5_EmptyPagesTriggersOCRRetry(t *testing.T) {
	// CT-5a: parseLiteParseJSON returns confidence=0.0 for empty pages.
	in := []byte(`{"pages":[]}`)
	_, conf, pageCount, err := parseLiteParseJSON(in)
	if err != nil {
		t.Fatalf("CT-5a: unexpected error: %v", err)
	}
	if conf != 0.0 {
		t.Errorf("CT-5a: confidence = %v, want 0.0 for empty pages", conf)
	}
	if pageCount != 0 {
		t.Errorf("CT-5a: pageCount = %d, want 0", pageCount)
	}

	// CT-5b: runLiteParse triggers OCR retry when confidence == 0.0 < threshold.
	calls := 0
	installExecLiteCmd(t, fakeLiteCmd([]struct {
		out []byte
		err error
	}{
		{out: []byte(`{"pages":[]}`), err: nil},                                     // first: empty pages
		{out: []byte(`{"pages":[{"text":"ocr text","confidence":0.7}]}`), err: nil}, // second: OCR retry
	}, &calls))

	_, _, err = runLiteParse(context.Background(), "fake.pdf", LiteParseConfig{ConfidenceThreshold: 0.5})
	if err != nil {
		t.Fatalf("CT-5b: unexpected error: %v", err)
	}
	if calls != 2 {
		t.Errorf("CT-5b: execLiteCmd called %d times, want 2 (empty pages → OCR retry)", calls)
	}
}

// --- CT-6: lit exit code 1 → error returned ---

func TestCT6_LitExitErrorReturned(t *testing.T) {
	installExecLiteCmd(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return nil, fmt.Errorf("exit status 1")
	})

	_, _, err := runLiteParse(context.Background(), "fake.pdf", LiteParseConfig{ConfidenceThreshold: 0.5})
	if err == nil {
		t.Error("CT-6: expected error when lit exits 1, got nil")
	}
}

// --- CT-7: LiteParseConfidenceThreshold config controls threshold ---

func TestCT7_ConfidenceThresholdConfigControlsRetry(t *testing.T) {
	// With threshold=0.7, confidence=0.6 is below threshold → OCR retry.
	calls := 0
	installExecLiteCmd(t, fakeLiteCmd([]struct {
		out []byte
		err error
	}{
		{out: []byte(`{"pages":[{"text":"text","confidence":0.6}]}`), err: nil},
		{out: []byte(`{"pages":[{"text":"text ocr","confidence":0.9}]}`), err: nil},
	}, &calls))

	_, _, err := runLiteParse(context.Background(), "fake.pdf", LiteParseConfig{ConfidenceThreshold: 0.7})
	if err != nil {
		t.Fatalf("CT-7: unexpected error: %v", err)
	}
	if calls != 2 {
		t.Errorf("CT-7: execLiteCmd called %d times, want 2 (confidence=0.6 < threshold=0.7)", calls)
	}
}

// --- CT-8: Confidence is the mean of per-page scores ---

func TestCT8_ConfidenceIsMeanOfPageScores(t *testing.T) {
	in := []byte(`{"pages":[{"text":"p1","confidence":0.8},{"text":"p2","confidence":0.6},{"text":"p3","confidence":0.4}]}`)
	_, conf, pageCount, err := parseLiteParseJSON(in)
	if err != nil {
		t.Fatalf("CT-8: unexpected error: %v", err)
	}
	if pageCount != 3 {
		t.Errorf("CT-8: pageCount = %d, want 3", pageCount)
	}
	// (0.8 + 0.6 + 0.4) / 3 = 0.6
	if conf < 0.599 || conf > 0.601 {
		t.Errorf("CT-8: confidence = %v, want ~0.6 (mean of 0.8, 0.6, 0.4)", conf)
	}
}

// --- BT-1: Text-native PDF (high confidence) → no OCR subprocess ---

func TestBT1_HighConfidenceNoOCRSubprocess(t *testing.T) {
	calls := 0
	installExecLiteCmd(t, fakeLiteCmd([]struct {
		out []byte
		err error
	}{
		{out: []byte(`{"pages":[{"text":"native text","confidence":0.95}]}`), err: nil},
	}, &calls))

	text, conf, err := runLiteParse(context.Background(), "native.pdf", LiteParseConfig{ConfidenceThreshold: 0.5})
	if err != nil {
		t.Fatalf("BT-1: unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("BT-1: %d lit calls, want 1 (text-native PDF must skip OCR)", calls)
	}
	if text != "native text" {
		t.Errorf("BT-1: text = %q, want %q", text, "native text")
	}
	_ = conf
}

// --- BT-2: scoreAsync writes confidence to queue row extraction_confidence ---

func TestBT2_ScoreAsyncWritesConfidenceToQueue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	q := newTestQueue(t)

	orig := execLiteParse
	defer func() { execLiteParse = orig }()
	execLiteParse = func(_ context.Context, _ string, _ LiteParseConfig) (string, float64, error) {
		return "extracted pdf text", 0.87, nil
	}

	req := &ShareRequest{
		Type:      "document",
		Profile:   "eng",
		AudioPath: "/dev/null",
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("BT-2: Enqueue: %v", err)
	}
	req.QueueRowID = id

	scoreAsync(req, q, &stubEvaluator{score: 60, verdict: "ok"}, nil, nil, nil)

	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("BT-2: GetByID: %v", err)
	}
	if item.ExtractionConfidence == nil {
		t.Fatal("BT-2: ExtractionConfidence is nil, want 0.87")
	}
	if *item.ExtractionConfidence < 0.869 || *item.ExtractionConfidence > 0.871 {
		t.Errorf("BT-2: ExtractionConfidence = %v, want ~0.87", *item.ExtractionConfidence)
	}
}

// --- BT-3: scoreAsync writes -1.0 on JSON parse fallback ---

func TestBT3_ScoreAsyncWritesNegativeOneOnFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	q := newTestQueue(t)

	orig := execLiteParse
	defer func() { execLiteParse = orig }()
	execLiteParse = func(_ context.Context, _ string, _ LiteParseConfig) (string, float64, error) {
		return "fallback text", -1.0, nil // -1.0 = JSON parse fallback sentinel
	}

	req := &ShareRequest{
		Type:      "document",
		Profile:   "eng",
		AudioPath: "/dev/null",
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("BT-3: Enqueue: %v", err)
	}
	req.QueueRowID = id

	scoreAsync(req, q, &stubEvaluator{score: 60, verdict: "ok"}, nil, nil, nil)

	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("BT-3: GetByID: %v", err)
	}
	if item.ExtractionConfidence == nil {
		t.Fatal("BT-3: ExtractionConfidence is nil, want -1.0")
	}
	if *item.ExtractionConfidence != -1.0 {
		t.Errorf("BT-3: ExtractionConfidence = %v, want -1.0 (JSON parse fallback)", *item.ExtractionConfidence)
	}
}

// --- RG-1: Non-PDF shares do not call execLiteParse ---

func TestRG1_NonPDFShareDoesNotCallLiteParse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	liteCalled := false
	orig := execLiteParse
	defer func() { execLiteParse = orig }()
	execLiteParse = func(_ context.Context, _ string, _ LiteParseConfig) (string, float64, error) {
		liteCalled = true
		return "", 0, errors.New("should not be called")
	}

	req := &ShareRequest{
		Type:    "url",
		Profile: "eng",
		URL:     "https://example.com/rg1",
	}
	scoreAsync(req, nil, &stubEvaluator{score: 70, verdict: "ok"}, nil, nil, nil)

	if liteCalled {
		t.Error("RG-1: execLiteParse was called for a non-document share")
	}
}

// --- RG-2: Plain-text fallback produces extracted text ---

func TestRG2_PlainTextFallbackProducesExtractedText(t *testing.T) {
	calls := 0
	installExecLiteCmd(t, fakeLiteCmd([]struct {
		out []byte
		err error
	}{
		{out: []byte(`not json`), err: nil},          // first: JSON format fails to parse
		{out: []byte(`extracted content`), err: nil}, // second: fallback --no-ocr -q
	}, &calls))

	text, conf, err := runLiteParse(context.Background(), "file.pdf", LiteParseConfig{ConfidenceThreshold: 0.5})
	if err != nil {
		t.Fatalf("RG-2: unexpected error: %v", err)
	}
	if text != "extracted content" {
		t.Errorf("RG-2: text = %q, want %q", text, "extracted content")
	}
	if conf != -1.0 {
		t.Errorf("RG-2: confidence = %v, want -1.0 (fallback sentinel)", conf)
	}
}
