package main

// EPIC-107 M2: unit test for exif_data multipart field handling.
// EPIC-103 M1: TestHandleShare_PDF_Multipart — HTTP-level PDF multipart integration test.
// EPIC-103 M2: TestHandleShare_PDF_Dedup — dedup within 5-minute window.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// buildPDFMultipart constructs a multipart/form-data body for a PDF share.
// filename and fileSize are required for dedup assertions (EPIC-078 M5).
func buildPDFMultipart(t *testing.T, action, filename string, fileSize int, withExif bool) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	w.WriteField("action", action)
	w.WriteField("mime_type", "application/pdf")
	w.WriteField("filename", filename)
	w.WriteField("file_size", fmt.Sprintf("%d", fileSize))
	if withExif {
		// Write a simulated EXIF field (binary-ish data).
		w.WriteField("exif_data", "\x00\x01\x02\x03fake-exif-bytes")
	}
	part, _ := w.CreateFormFile("file", filename)
	pdfBytes := make([]byte, fileSize)
	copy(pdfBytes, []byte("%PDF-1.4 fake pdf content for testing"))
	part.Write(pdfBytes)
	w.Close()
	return &body, w.FormDataContentType()
}

// --- EPIC-107 M2: exif_data field handled without error ----------------------

func TestHandleShare_ExifData_HandledWithoutError(t *testing.T) {
	// EPIC-107 M2: multipart body with exif_data field → server accepts, 200 OK.
	// The field is drained and logged at DEBUG level; no 400 or 500 response.
	installLiteParseStub(t, "pdf text", 0.9, nil)
	stubBackend := installHaikuJSONStub(t)

	cfg := builtinConfig()
	router := NewRouterFromConfig(&TmuxRunner{}, cfg, false)
	router.SetScoringBackend(stubBackend)
	q := newTestQueue(t)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	body, ct := buildPDFMultipart(t, "vnote_auto", "memo.pdf", 1024, true /* withExif */)
	req := httptest.NewRequest(http.MethodPost, "/share", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()

	srv.Mux().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("EPIC-107 M2: expected 200 with exif_data field, got %d: %s", rr.Code, rr.Body.String())
	}
}

// installHaikuJSONStub returns a scoring backend whose CompleteJSON returns a
// valid canned score, preventing real Claude CLI calls from background scoring
// goroutines. EPIC-258 M2: wire it with router.SetScoringBackend (or
// deps.Backend) instead of the former execHaikuJSON package-var swap.
func installHaikuJSONStub(t *testing.T) ScoringBackend {
	t.Helper()
	return jsonOnlyBackend(cannedVerdictJSON)
}

// --- EPIC-103 M1: PDF multipart integration test -----------------------------

func TestHandleShare_PDF_Multipart(t *testing.T) {
	// Asserts: HTTP 200, queue row with type="document" and mime_type="application/pdf".
	installLiteParseStub(t, "some extracted pdf text", 0.9, nil)
	stubBackend := installHaikuJSONStub(t)

	cfg := builtinConfig()
	router := NewRouterFromConfig(&TmuxRunner{}, cfg, false)
	router.SetScoringBackend(stubBackend)
	q := newTestQueue(t)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	const filename = "invoice_test.pdf"
	const fileSize = 2048
	body, ct := buildPDFMultipart(t, "vnote_auto", filename, fileSize, false)

	req := httptest.NewRequest(http.MethodPost, "/share", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()

	srv.Mux().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("EPIC-103 M1: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp ShareResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("EPIC-103 M1: decode response: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("EPIC-103 M1: response status = %q, want \"ok\"", resp.Status)
	}
	rowID := resp.ID
	if rowID == 0 {
		t.Fatal("EPIC-103 M1: expected non-zero queue row ID in response")
	}

	// Queue row should have type="document" (derived from mime_type=application/pdf).
	item, err := q.GetByID(rowID)
	if err != nil {
		t.Fatalf("EPIC-103 M1: GetByID(%d): %v", rowID, err)
	}
	if item.Type != "document" {
		t.Errorf("EPIC-103 M1: queue row type = %q, want \"document\" (derived from application/pdf)", item.Type)
	}

	// Verify mime_type column in DB (not in QueueItem struct — raw SQL).
	var mimeType string
	if err := q.db.QueryRow("SELECT COALESCE(mime_type,'') FROM queue WHERE id=?", rowID).Scan(&mimeType); err != nil {
		t.Fatalf("EPIC-103 M1: query mime_type: %v", err)
	}
	if mimeType != "application/pdf" {
		t.Errorf("EPIC-103 M1: queue row mime_type = %q, want \"application/pdf\"", mimeType)
	}
}

// --- EPIC-103 M2: PDF dedup within 5-minute window ---------------------------

func TestHandleShare_PDF_Dedup(t *testing.T) {
	// Asserts: second identical PDF (same filename + file_size) within 5-minute
	// window returns duplicate=true and HTTP 200.
	installLiteParseStub(t, "some pdf text", 0.9, nil)
	stubBackend := installHaikuJSONStub(t)

	cfg := builtinConfig()
	router := NewRouterFromConfig(&TmuxRunner{}, cfg, false)
	router.SetScoringBackend(stubBackend)
	q := newTestQueue(t)
	srv := NewServer("test-token", router, q, NewRingLog(10), false, nil)

	const filename = "statement_march.pdf"
	const fileSize = 4096

	doShare := func() *httptest.ResponseRecorder {
		body, ct := buildPDFMultipart(t, "vnote_auto", filename, fileSize, false)
		req := httptest.NewRequest(http.MethodPost, "/share", body)
		req.Header.Set("Content-Type", ct)
		req.Header.Set("Authorization", "Bearer test-token")
		rr := httptest.NewRecorder()
		srv.Mux().ServeHTTP(rr, req)
		return rr
	}

	// First share — should succeed and create a queue row.
	rr1 := doShare()
	if rr1.Code != http.StatusOK {
		t.Fatalf("EPIC-103 M2: first share: expected 200, got %d: %s", rr1.Code, rr1.Body.String())
	}
	var resp1 ShareResponse
	json.NewDecoder(rr1.Body).Decode(&resp1)
	if resp1.ID == 0 {
		t.Fatal("EPIC-103 M2: first share should return a non-zero queue ID")
	}

	// Brief pause to ensure the queue row is committed before the second share.
	time.Sleep(10 * time.Millisecond)

	// Second share with same filename+fileSize within 5 minutes → dedup.
	rr2 := doShare()
	if rr2.Code != http.StatusOK {
		t.Fatalf("EPIC-103 M2: second share: expected 200, got %d: %s", rr2.Code, rr2.Body.String())
	}
	var resp2 ShareResponse
	json.NewDecoder(rr2.Body).Decode(&resp2)
	if !resp2.Duplicate {
		t.Errorf("EPIC-103 M2: second identical PDF share should return duplicate=true; got %+v", resp2)
	}
	if resp2.Message != "duplicate" {
		t.Errorf("EPIC-103 M2: duplicate response message = %q, want \"duplicate\"", resp2.Message)
	}
	if resp2.ID != resp1.ID {
		t.Errorf("EPIC-103 M2: dedup response ID = %d, want %d (original row)", resp2.ID, resp1.ID)
	}
}
