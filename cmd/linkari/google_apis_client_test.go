package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// CT-10: compile-time assertion that GoogleAPIsClient satisfies DomainClient.
var _ DomainClient = (*GoogleAPIsClient)(nil)

// CT-1: ParseGoogleURL drive.google.com/file/d/{ID}/view → (GoogleDrive, ID, nil)
func TestParseGoogleURL_DriveFile(t *testing.T) {
	u, _ := url.Parse("https://drive.google.com/file/d/abc123/view")
	svc, id, err := ParseGoogleURL(u)
	if err != nil {
		t.Fatalf("CT-1: unexpected error: %v", err)
	}
	if svc != GoogleDrive || id != "abc123" {
		t.Errorf("CT-1: got (%v, %q), want (GoogleDrive, abc123)", svc, id)
	}
}

// CT-2: ParseGoogleURL docs.google.com/document/d/{ID}/edit → (GoogleDrive, ID, nil)
func TestParseGoogleURL_DocsDocument(t *testing.T) {
	u, _ := url.Parse("https://docs.google.com/document/d/docID456/edit")
	svc, id, err := ParseGoogleURL(u)
	if err != nil {
		t.Fatalf("CT-2: unexpected error: %v", err)
	}
	if svc != GoogleDrive || id != "docID456" {
		t.Errorf("CT-2: got (%v, %q), want (GoogleDrive, docID456)", svc, id)
	}
}

// CT-3: ParseGoogleURL docs.google.com/spreadsheets/d/{ID} → (GoogleDrive, ID, nil)
func TestParseGoogleURL_Spreadsheet(t *testing.T) {
	u, _ := url.Parse("https://docs.google.com/spreadsheets/d/sheetID789")
	svc, id, err := ParseGoogleURL(u)
	if err != nil {
		t.Fatalf("CT-3: unexpected error: %v", err)
	}
	if svc != GoogleDrive || id != "sheetID789" {
		t.Errorf("CT-3: got (%v, %q), want (GoogleDrive, sheetID789)", svc, id)
	}
}

// CT-4: ParseGoogleURL drive.google.com/drive/folders/{ID} → ErrUnsupportedGoogleURL
func TestParseGoogleURL_FolderUnsupported(t *testing.T) {
	u, _ := url.Parse("https://drive.google.com/drive/folders/folderID")
	_, _, err := ParseGoogleURL(u)
	if err != ErrUnsupportedGoogleURL {
		t.Errorf("CT-4: expected ErrUnsupportedGoogleURL, got %v", err)
	}
}

// CT-5: ParseGoogleURL google.com/search?q=foo → ErrUnsupportedGoogleURL
func TestParseGoogleURL_SearchUnsupported(t *testing.T) {
	u, _ := url.Parse("https://google.com/search?q=foo")
	_, _, err := ParseGoogleURL(u)
	if err != ErrUnsupportedGoogleURL {
		t.Errorf("CT-5: expected ErrUnsupportedGoogleURL, got %v", err)
	}
}

// mockGoogleServer dispatches to metaHandler or contentHandler based on path/query.
func mockGoogleServer(metaHandler, contentHandler http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "alt=media") || strings.Contains(r.URL.Path, "/export") {
			contentHandler(w, r)
		} else {
			metaHandler(w, r)
		}
	}))
}

func googleTestClient(srv *httptest.Server) *GoogleAPIsClient {
	return &GoogleAPIsClient{client: srv.Client(), apiBase: srv.URL}
}

// CT-6: Fetch mock 200 Drive → (content, ContentTypePlain, nil)
func TestGoogleAPIsClient_Fetch_OK(t *testing.T) {
	srv := mockGoogleServer(
		func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"mimeType": "application/pdf"})
		},
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("file content"))
		},
	)
	defer srv.Close()

	c := googleTestClient(srv)
	u, _ := url.Parse("https://drive.google.com/file/d/fileID/view")
	content, ct, err := c.Fetch(context.Background(), u)
	if err != nil {
		t.Fatalf("CT-6: unexpected error: %v", err)
	}
	if ct != ContentTypePlain {
		t.Errorf("CT-6: expected ContentTypePlain, got %v", ct)
	}
	if content != "file content" {
		t.Errorf("CT-6: expected 'file content', got %q", content)
	}
}

// CT-7: Fetch mock 401 → ErrGoogleAuth
func TestGoogleAPIsClient_Fetch_Auth401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := googleTestClient(srv)
	u, _ := url.Parse("https://drive.google.com/file/d/fileID/view")
	_, _, err := c.Fetch(context.Background(), u)
	if err != ErrGoogleAuth {
		t.Errorf("CT-7: expected ErrGoogleAuth, got %v", err)
	}
}

// CT-8: Fetch mock 404 → ErrGoogleNotFound
func TestGoogleAPIsClient_Fetch_NotFound404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := googleTestClient(srv)
	u, _ := url.Parse("https://drive.google.com/file/d/fileID/view")
	_, _, err := c.Fetch(context.Background(), u)
	if err != ErrGoogleNotFound {
		t.Errorf("CT-8: expected ErrGoogleNotFound, got %v", err)
	}
}

// CT-9: Fetch mock 429 → ErrGoogleQuotaExceeded
func TestGoogleAPIsClient_Fetch_Quota429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := googleTestClient(srv)
	u, _ := url.Parse("https://drive.google.com/file/d/fileID/view")
	_, _, err := c.Fetch(context.Background(), u)
	if err != ErrGoogleQuotaExceeded {
		t.Errorf("CT-9: expected ErrGoogleQuotaExceeded, got %v", err)
	}
}

// CT-11: ParseGoogleURL docs.google.com/presentation/d/{ID} → (GoogleDrive, ID, nil)
func TestParseGoogleURL_Presentation(t *testing.T) {
	u, _ := url.Parse("https://docs.google.com/presentation/d/presID/edit")
	svc, id, err := ParseGoogleURL(u)
	if err != nil {
		t.Fatalf("CT-11: unexpected error: %v", err)
	}
	if svc != GoogleDrive || id != "presID" {
		t.Errorf("CT-11: got (%v, %q), want (GoogleDrive, presID)", svc, id)
	}
}

// CT-12: NewGoogleAPIsClient with invalid tokenJSON → ErrGoogleTokenInvalid
func TestNewGoogleAPIsClient_InvalidToken(t *testing.T) {
	_, err := NewGoogleAPIsClient("id", "secret", "not-valid-json")
	if err != ErrGoogleTokenInvalid {
		t.Errorf("CT-12: expected ErrGoogleTokenInvalid, got %v", err)
	}
}

// BT-1: export endpoint used for vnd.google-apps.document mimeType.
func TestGoogleAPIsClient_FetchDriveFile_ExportGoogleApps(t *testing.T) {
	exportCalled := false
	srv := mockGoogleServer(
		func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"mimeType": "application/vnd.google-apps.document"})
		},
		func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/export") {
				exportCalled = true
			}
			_, _ = w.Write([]byte("exported text"))
		},
	)
	defer srv.Close()

	c := googleTestClient(srv)
	content, err := c.FetchDriveFile(context.Background(), "docID")
	if err != nil {
		t.Fatalf("BT-1: unexpected error: %v", err)
	}
	if !exportCalled {
		t.Error("BT-1: expected export endpoint for Google Workspace mimeType")
	}
	if content != "exported text" {
		t.Errorf("BT-1: expected 'exported text', got %q", content)
	}
}

// BT-2: alt=media used for application/pdf mimeType.
func TestGoogleAPIsClient_FetchDriveFile_AltMediaPDF(t *testing.T) {
	altMediaCalled := false
	srv := mockGoogleServer(
		func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"mimeType": "application/pdf"})
		},
		func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.RawQuery, "alt=media") {
				altMediaCalled = true
			}
			_, _ = w.Write([]byte("pdf bytes"))
		},
	)
	defer srv.Close()

	c := googleTestClient(srv)
	_, err := c.FetchDriveFile(context.Background(), "pdfID")
	if err != nil {
		t.Fatalf("BT-2: unexpected error: %v", err)
	}
	if !altMediaCalled {
		t.Error("BT-2: expected alt=media for non-Google-Workspace mimeType")
	}
}

// BT-3: token string never appears in any onEvent callback fields.
func TestGoogleAPIsClient_Fetch_TokenNotLeaked(t *testing.T) {
	const fakeToken = "super-secret-token-xyz"
	srv := mockGoogleServer(
		func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"mimeType": "application/pdf"})
		},
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("content"))
		},
	)
	defer srv.Close()

	var payloads []string
	c := googleTestClient(srv)
	c.onEvent = func(e Event) {
		b, _ := json.Marshal(e)
		payloads = append(payloads, string(b))
	}
	u, _ := url.Parse("https://drive.google.com/file/d/fileID/view")
	_, _, _ = c.Fetch(context.Background(), u)

	for _, p := range payloads {
		if strings.Contains(p, fakeToken) {
			t.Errorf("BT-3: token leaked in event payload: %s", p)
		}
	}
}

// RG-1: Any docs.google.com URL → ParseGoogleURL returns GoogleDrive.
func TestParseGoogleURL_RG1_DocsAlwaysDrive(t *testing.T) {
	cases := []string{
		"https://docs.google.com/document/d/id1/edit",
		"https://docs.google.com/spreadsheets/d/id2",
		"https://docs.google.com/presentation/d/id3/edit",
	}
	for _, raw := range cases {
		u, _ := url.Parse(raw)
		svc, _, err := ParseGoogleURL(u)
		if err != nil {
			t.Errorf("RG-1: %s → unexpected error: %v", raw, err)
			continue
		}
		if svc != GoogleDrive {
			t.Errorf("RG-1: %s → expected GoogleDrive, got %v", raw, svc)
		}
	}
}

// RG-2: ErrGoogleQuotaExceeded from stubbed client → DomainRouter falls back to jinaFetch.
func TestDomain_RG2_GoogleQuotaFallsBackToJina(t *testing.T) {
	quota := &mockDomainClient{
		fetchFn: func(ctx context.Context, u *url.URL) (string, ContentType, error) {
			return "", ContentTypePlain, ErrGoogleQuotaExceeded
		},
	}
	jinaFetched := false
	jinaFn := func(ctx context.Context, rawURL string) (string, error) {
		jinaFetched = true
		return "jina-content", nil
	}
	dr := NewDomainRouter(map[string]DomainClient{"drive.google.com": quota}, jinaFn)

	_, _, err := dr.FetchWithFallback(context.Background(), "https://drive.google.com/file/d/abc/view")
	if err != nil {
		t.Fatalf("RG-2: unexpected error: %v", err)
	}
	if !jinaFetched {
		t.Error("RG-2: expected DomainRouter to fall back to jinaFetch on ErrGoogleQuotaExceeded")
	}
}
