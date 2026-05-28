package main

// EPIC-105: PDF Profile Routing and ContentTypePDF Scoring Preamble
//
// Contract Tests (CT-1 through CT-7): written before implementation per M1 gate.
// Behavioral Tests (BT-1, BT-2) and Regression Guards (RG-1, RG-2): M6 additions.
//
// Coverage:
//   CT-1: PDF + CategoryFinance → classifyByIntentMetadata returns "finance"
//   CT-2: PDF + AppCategory=0 → falls through, returns ""
//   CT-3: Non-PDF MIME + CategoryFinance → existing behavior (not "finance")
//   CT-4: type=document → sysPrompt starts with ContentTypePDF
//   CT-5: type=url → sysPrompt does NOT start with ContentTypePDF
//   CT-6: ContentTypeMarkdown preamble unchanged for URL shares
//   CT-7: AppCategory=0 + PDF → classifyByIntentMetadata returns "", filename cascade runs
//   BT-1: Finance-category PDF → classifyByIntentMetadata returns "finance"
//   BT-2: Non-finance PDF → classifyByIntentMetadata returns "" (falls through, no error)
//   RG-1: mimeProfileMap entries unchanged by new branch
//   RG-2: URL share with CategoryFinance but no PDF mime_type → not routed via PDF heuristic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureEval records the promptTemplate arg from the last Evaluate call.
type captureEval struct {
	content string
	prompt  string
}

func (e *captureEval) Name() string { return "capture" }
func (e *captureEval) Evaluate(_ context.Context, content, promptTemplate string) (*Scorecard, error) {
	e.content = content
	e.prompt = promptTemplate
	return &Scorecard{Score: 75, Verdict: "ok", SourceType: "test"}, nil
}

// --- CT-1: PDF + CategoryFinance → "finance" ----------------------------------

func TestCT1_PDF_FinanceCategory_RoutesToFinance(t *testing.T) {
	req := &ShareRequest{MimeType: "application/pdf", AppCategory: CategoryFinance}
	if got := classifyByIntentMetadata(req); got != "finance" {
		t.Errorf("CT-1: classifyByIntentMetadata(pdf+cat6) = %q, want \"finance\"", got)
	}
}

// --- CT-2: PDF + AppCategory=0 → falls through, returns "" -------------------

func TestCT2_PDF_UnknownCategory_FallsThrough(t *testing.T) {
	req := &ShareRequest{MimeType: "application/pdf", AppCategory: 0}
	if got := classifyByIntentMetadata(req); got != "" {
		t.Errorf("CT-2: classifyByIntentMetadata(pdf+cat0) = %q, want \"\" (fallthrough)", got)
	}
}

// --- CT-3: Non-PDF MIME + CategoryFinance → unchanged behavior ---------------

func TestCT3_NonPDF_FinanceCategory_Unchanged(t *testing.T) {
	req := &ShareRequest{MimeType: "text/plain", AppCategory: CategoryFinance}
	got := classifyByIntentMetadata(req)
	// text/plain is not in mimeProfileMap; category 6 → "travel" via appCategoryProfileMap.
	if got == "finance" {
		t.Errorf("CT-3: non-PDF mime with CategoryFinance should not return \"finance\"; got %q", got)
	}
}

// --- CT-4: type=document → sysPrompt starts with ContentTypePDF --------------

func TestCT4_ContentTypePDF_PrependedForDocument(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installLiteParseStub(t, "extracted pdf text about finance and invoices", 0.9, nil)

	cap := &captureEval{}
	req := &ShareRequest{
		Type:      "document",
		Profile:   "eng",
		MimeType:  "application/pdf",
		AudioPath: "/dev/null",
	}
	scoreAsync(req, nil, cap, nil, nil, nil)

	if !strings.HasPrefix(cap.prompt, ContentTypePDF) {
		prefix := cap.prompt
		if len(prefix) > 200 {
			prefix = prefix[:200]
		}
		t.Errorf("CT-4: sysPrompt for type=document should start with ContentTypePDF\nfirst 200 chars: %q", prefix)
	}
}

// --- CT-5: type=url → sysPrompt does NOT start with ContentTypePDF -----------

func TestCT5_ContentTypePDF_NotPrependedForURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	isolateEventsDir(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Some URL page content about machine learning and AI."))
	}))
	t.Cleanup(srv.Close)
	installJinaServer(t, srv)

	cap := &captureEval{}
	req := &ShareRequest{
		Type:    "url",
		Profile: "eng",
		URL:     "https://example.com/article",
	}
	scoreAsync(req, nil, cap, nil, nil, nil)

	if strings.HasPrefix(cap.prompt, ContentTypePDF) {
		t.Error("CT-5: sysPrompt for type=url should NOT start with ContentTypePDF")
	}
}

// --- CT-6: ContentTypeMarkdown preamble unchanged ----------------------------

func TestCT6_ContentTypeMarkdown_Unchanged(t *testing.T) {
	// CT-6: classificationPreamble with ContentTypeMarkdown still returns the
	// markdown hint and does not start with ContentTypePDF.
	preamble := classificationPreamble("eng", "https://github.com/foo/bar", "url_domain", ContentTypeMarkdown)
	if strings.HasPrefix(preamble, ContentTypePDF) {
		t.Error("CT-6: classificationPreamble(ContentTypeMarkdown) must not start with ContentTypePDF")
	}
	if !strings.Contains(preamble, "markdown") {
		t.Error("CT-6: ContentTypeMarkdown preamble must contain 'markdown'")
	}
}

// --- CT-7: AppCategory=0 + PDF → classifyByIntentMetadata returns "" ---------

func TestCT7_EmptyCategory_FallsThrough_FilenameRunsNext(t *testing.T) {
	// classifyByIntentMetadata returns "" for PDF with AppCategory=0.
	req := &ShareRequest{MimeType: "application/pdf", AppCategory: 0, Filename: "invoice_2024.pdf"}
	if got := classifyByIntentMetadata(req); got != "" {
		t.Errorf("CT-7: classifyByIntentMetadata(pdf, cat=0) = %q, want \"\" (should fall through)", got)
	}
	// Filename cascade (classifyByFilename) can still classify when intent misses.
	if got := classifyByFilename(req.Filename); got != "finance" {
		t.Errorf("CT-7: classifyByFilename(%q) = %q, want \"finance\" (cascade runs after fallthrough)", req.Filename, got)
	}
}

// --- BT-1: Finance-category PDF classified to "finance" ----------------------

func TestBT1_FinanceCategoryPDF_ClassifiedToFinanceProfile(t *testing.T) {
	req := &ShareRequest{MimeType: "application/pdf", AppCategory: CategoryFinance}
	if got := classifyByIntentMetadata(req); got != "finance" {
		t.Errorf("BT-1: finance PDF should route to \"finance\" profile, got %q", got)
	}
}

// --- BT-2: Non-finance PDF → falls through without error ---------------------

func TestBT2_NonFinancePDF_NoError(t *testing.T) {
	req := &ShareRequest{MimeType: "application/pdf", AppCategory: 0}
	got := classifyByIntentMetadata(req)
	if got == "finance" {
		t.Errorf("BT-2: non-finance PDF should not route to \"finance\", got %q", got)
	}
}

// --- RG-1: mimeProfileMap entries unchanged ----------------------------------

func TestRG1_MimeProfileMap_Unchanged(t *testing.T) {
	cases := []struct {
		mime string
		want string
	}{
		{"application/vnd.ms-excel", "finance"},
		{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "finance"},
		{"text/x-vcard", "life"},
		{"application/pdf", ""},  // intentionally excluded from mimeProfileMap; AppCategory=0 here
	}
	for _, c := range cases {
		t.Run(c.mime, func(t *testing.T) {
			req := &ShareRequest{MimeType: c.mime, AppCategory: 0}
			got := classifyByIntentMetadata(req)
			if got != c.want {
				t.Errorf("RG-1: classifyByIntentMetadata(mime=%q, cat=0) = %q, want %q", c.mime, got, c.want)
			}
		})
	}
}

// --- RG-2: URL share with CategoryFinance and no PDF mime → not finance ------

func TestRG2_URLShare_CategoryFinance_NoPDFMime_NotFinance(t *testing.T) {
	// URL share with category 6 but mime_type="" → appCategoryProfileMap → "travel",
	// not "finance" (PDF heuristic requires mime_type=application/pdf).
	req := &ShareRequest{Type: "url", URL: "https://example.com", AppCategory: CategoryFinance, MimeType: ""}
	got := classifyByIntentMetadata(req)
	if got == "finance" {
		t.Errorf("RG-2: URL share with CategoryFinance and no PDF mime should not be \"finance\"; got %q", got)
	}
}

// --- RG-3 (POMO_20260526T202824Z_pdf-action-routing-gap): bare "note" from Android --

// TestRG3_BareNoteAction_PDFShare_RoutesOK guards against the silent routing
// failure from trace_id 5382e37d where every PDF file share sent action=note
// and received HTTP 200 + no scoring. The Android share sheet sends the bare
// action string "note" for PDF file shares; the server must normalize this to
// note_auto via bare-intent normalization and complete routing without error.
func TestRG3_BareNoteAction_PDFShare_RoutesOK(t *testing.T) {
	// Redirect transcriptDir so the background scoreAsync goroutine spawned by
	// ServeHTTP writes to a temp dir rather than the real transcripts directory.
	prevTranscriptDir := transcriptDir
	transcriptDir = t.TempDir()
	t.Cleanup(func() { transcriptDir = prevTranscriptDir })

	installLiteParseStub(t, "extracted pdf text", 0.9, nil)
	installHaikuJSONStub(t)

	cfg := builtinConfig()
	router := NewRouterFromConfig(&TmuxRunner{}, cfg, false)
	q := newTestQueue(t)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	body, ct := buildPDFMultipart(t, "note", "paperwork.pdf", 143545, false)
	req := httptest.NewRequest(http.MethodPost, "/share", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()

	srv.Mux().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("RG-3: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp ShareResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("RG-3: decode response: %v", err)
	}
	if resp.Status == "queued" {
		t.Errorf("RG-3: response status=%q indicates routing failure; action=note must resolve to note_auto", resp.Status)
	}
	if resp.Status != "ok" {
		t.Errorf("RG-3: response status=%q, want \"ok\"", resp.Status)
	}
	if resp.ID == 0 {
		t.Fatal("RG-3: expected non-zero queue row ID in response")
	}

	item, err := q.GetByID(resp.ID)
	if err != nil {
		t.Fatalf("RG-3: GetByID(%d): %v", resp.ID, err)
	}
	if item.Type != "document" {
		t.Errorf("RG-3: queue row type=%q, want \"document\"", item.Type)
	}
}

// TestRG3b_UnknownAction_Returns400 guards the no-queue case: when no queue is
// active and the action has no ActionConfig entry, the server must return 400
// rather than 500. With a queue, legacy action names are queued for replay per
// backward-compat RG-1 (TestCompat_RG1_ActionOnlyNotRejected).
func TestRG3b_UnknownAction_Returns400(t *testing.T) {
	cfg := builtinConfig()
	router := NewRouterFromConfig(&TmuxRunner{}, cfg, false)
	srv := NewServer("test-token", router, nil, NewRingLog(10), false, nil)

	body, ct := buildPDFMultipart(t, "totally_unknown_action_xyz", "doc.pdf", 1024, false)
	req := httptest.NewRequest(http.MethodPost, "/share", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()

	srv.Mux().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("RG-3b: unknown action should return 400, got %d: %s", rr.Code, rr.Body.String())
	}
}
