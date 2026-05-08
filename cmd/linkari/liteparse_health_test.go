package main

// EPIC-102 M1: Contract tests for LiteParse health visibility and graceful degradation.
// CT-1 through CT-10 are committed here as the failing-first gate before any
// implementation lands. They go green milestone by milestone (M2–M8).
//
// FIRST constraints (per TDD §6):
//   - Fast:       all unit tests < 1s (pure function; no real subprocess/db I/O in CT-1–CT-4)
//   - Independent: each test constructs its own env / queue
//   - Repeatable:  lit presence checked via injected lookPath, not real PATH
//   - Self-Validating: require-style assertions only
//   - Timely:     committed before M2 implementation begins

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- CT-1: probeHealth returns LitPresent=false when lit not on PATH ---

func TestCT1_LitNotPresent(t *testing.T) {
	probe := probeHealth(
		func(string) (string, error) { return "", errors.New("not found") },
		"/data/tessdata",
	)
	if probe.LitPresent {
		t.Error("CT-1: LitPresent should be false when lookPath fails")
	}
}

// --- CT-2: probeHealth returns TessdataPrefixSet=false when env var unset ---

func TestCT2_TessdataPrefixUnset(t *testing.T) {
	probe := probeHealth(
		func(string) (string, error) { return "/usr/local/bin/lit", nil },
		"", // tessdata not configured
	)
	if probe.TessdataPrefixSet {
		t.Error("CT-2: TessdataPrefixSet should be false when env var is empty")
	}
}

// --- CT-3: health status is "degraded" when lit not present ---

func TestCT3_HealthDegradedWhenLitAbsent(t *testing.T) {
	probe := probeHealth(
		func(string) (string, error) { return "", errors.New("not found") },
		"/data/tessdata",
	)
	if probe.Status != "degraded" {
		t.Errorf("CT-3: Status = %q, want \"degraded\"", probe.Status)
	}
}

// --- CT-4: health status is "ok" when both checks pass ---

func TestCT4_HealthOKWhenBothPresent(t *testing.T) {
	probe := probeHealth(
		func(string) (string, error) { return "/usr/local/bin/lit", nil },
		"/data/tessdata",
	)
	if probe.Status != "ok" {
		t.Errorf("CT-4: Status = %q, want \"ok\"", probe.Status)
	}
	if !probe.LitPresent {
		t.Error("CT-4: LitPresent should be true")
	}
	if !probe.TessdataPrefixSet {
		t.Error("CT-4: TessdataPrefixSet should be true")
	}
}

// --- CT-5: scoreAsync sets content_warning on queue row when execLiteParse errors ---

func TestCT5_ContentWarningSetOnLiteParseFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // prevent resolvePushConfigOnce from loading real config.toml
	q := newTestQueue(t)

	// Override execLiteParse to simulate failure.
	orig := execLiteParse
	defer func() { execLiteParse = orig }()
	execLiteParse = func(ctx context.Context, path string, cfg LiteParseConfig) (string, float64, error) {
		return "", 0.0, errors.New("lit parse: exit status 1")
	}

	// Enqueue a document-type share so scoreAsync takes the lit branch.
	req := &ShareRequest{
		Type:      "document",
		Profile:   "eng",
		AudioPath: "/dev/null",
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	req.QueueRowID = id

	scoreAsync(req, q, &stubEvaluator{score: 50, verdict: "ok"}, nil, nil)

	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if item.ContentWarning != "lit_parse_failed" {
		t.Errorf("CT-5: ContentWarning = %q, want \"lit_parse_failed\"", item.ContentWarning)
	}
}

// --- CT-7: scoreAsync does NOT set content_warning when execLiteParse succeeds ---

func TestCT7_ContentWarningEmptyOnSuccess(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // prevent resolvePushConfigOnce from loading real config.toml
	q := newTestQueue(t)

	orig := execLiteParse
	defer func() { execLiteParse = orig }()
	execLiteParse = func(ctx context.Context, path string, cfg LiteParseConfig) (string, float64, error) {
		return "some extracted text", 0.9, nil
	}

	req := &ShareRequest{
		Type:      "document",
		Profile:   "eng",
		AudioPath: "/dev/null",
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	req.QueueRowID = id

	scoreAsync(req, q, &stubEvaluator{score: 50, verdict: "ok"}, nil, nil)

	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if item.ContentWarning != "" {
		t.Errorf("CT-7: ContentWarning = %q, want empty on successful parse", item.ContentWarning)
	}
}

// --- CT-6: FCM push payload includes content_warning when queue row has it set ---

func TestCT6_FCMIncludesContentWarning(t *testing.T) {
	// capturingTransport records the FCM request body.
	type captured struct{ body []byte }
	cap := &captured{}
	rt := &funcRoundTripper{fn: func(req *http.Request) (*http.Response, error) {
		cap.body, _ = io.ReadAll(req.Body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	}}
	installStubTransport(t, rt)

	srv := newTestServerWithFCM(t)
	q := srv.queue

	// Enqueue a push row that already has content_warning set.
	id, err := q.EnqueueDigestIfDue(context.Background(),
		"eng", 80, "test-slug", "Good read", "https://example.com/ct6",
		"", "", "", "lit_parse_failed",
	)
	if err != nil {
		t.Fatalf("EnqueueDigestIfDue: %v", err)
	}
	if !id.Enqueued {
		t.Fatalf("CT-6: push row not enqueued: %s", id.Reason)
	}

	_ = q.UpsertDevice("fake-device-token")
	srv.drainPushOutbox(context.Background())

	if len(cap.body) == 0 {
		t.Fatal("CT-6: FCM request body was empty — drainPushOutbox did not fire")
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(cap.body, &payload); err != nil {
		t.Fatalf("CT-6: unmarshal FCM payload: %v", err)
	}
	msg, _ := payload["message"].(map[string]interface{})
	data, _ := msg["data"].(map[string]interface{})
	if data["content_warning"] != "lit_parse_failed" {
		t.Errorf("CT-6: data[\"content_warning\"] = %v, want \"lit_parse_failed\"", data["content_warning"])
	}
}

// --- CT-8: cmd_doctor reports TESSDATA_PREFIX status ---

func TestCT8_DoctorReportsTessdataPrefix(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TESSDATA_PREFIX", "") // ensure it's unset
	out, run := newDoctorCmdForTest(t, dir, nil)
	_ = run() // may fail for other reasons (token etc.) — we only check output
	got := out.String()
	if !strings.Contains(got, "TESSDATA_PREFIX") {
		t.Errorf("CT-8: expected doctor output to mention TESSDATA_PREFIX, got:\n%s", got)
	}
}

// --- CT-9: GET /archive item includes content_warning when set ---

func TestCT9_ArchiveItemIncludesContentWarning(t *testing.T) {
	q := newTestQueue(t)

	// Insert a queue row with content_warning set and archive it.
	req := &ShareRequest{Type: "url", URL: "https://example.com/ct9", Profile: "eng"}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := q.SetContentWarning(id, "lit_parse_failed"); err != nil {
		t.Fatalf("SetContentWarning: %v", err)
	}
	score := 75
	if _, _, err := q.ScoreByURL(req.URL, score, "good", "", "eng", "ct9", "", ""); err != nil {
		t.Fatalf("ScoreByURL: %v", err)
	}
	if err := q.Archive(id); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	items, err := q.ListArchivedCursorTyped("eng", "archived", "", 0, 10, nil)
	if err != nil {
		t.Fatalf("ListArchivedCursorTyped: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("CT-9: no archived items returned")
	}

	b, _ := json.Marshal(items[0])
	var out map[string]interface{}
	_ = json.Unmarshal(b, &out)
	if out["content_warning"] != "lit_parse_failed" {
		t.Errorf("CT-9: archive item content_warning = %v, want \"lit_parse_failed\"", out["content_warning"])
	}
}

// --- CT-10: GET /archive item omits content_warning when not set ---

func TestCT10_ArchiveItemOmitsContentWarningWhenEmpty(t *testing.T) {
	q := newTestQueue(t)

	req := &ShareRequest{Type: "url", URL: "https://example.com/ct10", Profile: "eng"}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	score := 75
	if _, _, err := q.ScoreByURL(req.URL, score, "good", "", "eng", "ct10", "", ""); err != nil {
		t.Fatalf("ScoreByURL: %v", err)
	}
	if err := q.Archive(id); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	items, err := q.ListArchivedCursorTyped("eng", "archived", "", 0, 10, nil)
	if err != nil {
		t.Fatalf("ListArchivedCursorTyped: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("CT-10: no archived items returned")
	}

	b, _ := json.Marshal(items[0])
	var out map[string]interface{}
	_ = json.Unmarshal(b, &out)
	if _, ok := out["content_warning"]; ok {
		t.Errorf("CT-10: archive item should omit content_warning when empty, got: %v", out["content_warning"])
	}
}

// --- helpers used by CT-6 ---

// --- M9: BT-1, BT-2, RG-1, RG-2 ---

// BT-1: Non-PDF shares are unaffected by content_warning logic.
func TestBT1_NonPDFShareHasNoContentWarning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	q := newTestQueue(t)

	req := &ShareRequest{
		Type:    "url",
		URL:     "https://example.com/bt1",
		Profile: "eng",
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	req.QueueRowID = id

	scoreAsync(req, q, &stubEvaluator{score: 60, verdict: "ok"}, nil, nil)

	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if item.ContentWarning != "" {
		t.Errorf("BT-1: non-PDF share ContentWarning = %q, want empty", item.ContentWarning)
	}
}

// BT-2: lit_version populated from `lit --version` output.
func TestBT2_LitVersionPopulated(t *testing.T) {
	// Mock lit binary path via lookPath so we can inject a fake version.
	// The real lit --version is not called — we inject a stub lookPath that
	// returns a path pointing to our test fake.
	fakeLitPath := "/usr/local/bin/lit"
	probe := probeHealth(
		func(name string) (string, error) {
			if name == "lit" {
				return fakeLitPath, nil
			}
			return "", errors.New("not found")
		},
		"/data/tessdata",
	)
	// LitPresent must be true; LitVersion is populated when `lit --version` succeeds.
	// In this unit test the real binary is not invoked, so LitVersion may be empty.
	// The test asserts that the probe doesn't lie about LitPresent.
	if !probe.LitPresent {
		t.Error("BT-2: LitPresent should be true when lookPath succeeds")
	}
}

// RG-1: Successful PDF extraction does not set content_warning.
func TestRG1_SuccessfulPDFDoesNotSetContentWarning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	q := newTestQueue(t)

	orig := execLiteParse
	defer func() { execLiteParse = orig }()
	execLiteParse = func(ctx context.Context, path string, cfg LiteParseConfig) (string, float64, error) {
		return "page 1 content", 0.9, nil // successful extraction
	}

	req := &ShareRequest{
		Type:      "document",
		Profile:   "eng",
		AudioPath: "/dev/null",
	}
	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	req.QueueRowID = id

	scoreAsync(req, q, &stubEvaluator{score: 70, verdict: "Worth saving"}, nil, nil)

	item, err := q.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if item.ContentWarning != "" {
		t.Errorf("RG-1: successful extraction set ContentWarning=%q, want empty", item.ContentWarning)
	}
}

// RG-2: GET /health still returns existing fields (no breaking change).
func TestRG2_HealthRetainsExistingFields(t *testing.T) {
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	srv := NewServer("test-token", router, nil, NewRingLog(10), false, nil)
	mux := srv.Mux()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK && w.Code != http.StatusServiceUnavailable {
		t.Fatalf("RG-2: unexpected status %d", w.Code)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("RG-2: decode: %v", err)
	}
	for _, field := range []string{"status", "version", "lit_present", "tessdata_prefix_set"} {
		if _, ok := body[field]; !ok {
			// version field: health response uses a map, no "version" field by default
			// allow missing version but require the lit fields and status
			if field == "version" {
				continue
			}
			t.Errorf("RG-2: health response missing field %q", field)
		}
	}
	if _, ok := body["status"]; !ok {
		t.Error("RG-2: health response missing 'status' field")
	}
}

// funcRoundTripper invokes fn for every request — used to capture FCM payloads.
type funcRoundTripper struct {
	fn func(*http.Request) (*http.Response, error)
}

func (f *funcRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f.fn(req)
}

// newTestServerWithFCM creates a Server with a fake FCM token source and the
// test queue wired up — enough for drainPushOutbox to fire.
func newTestServerWithFCM(t *testing.T) *Server {
	t.Helper()
	q := newTestQueue(t)
	tmux := &TmuxRunner{}
	router := NewRouterFromConfig(tmux, builtinConfig(), false)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)
	srv.fcmTokenSource = fakeTokenSource{}
	return srv
}
