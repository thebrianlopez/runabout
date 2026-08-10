package main

// EPIC-076 M3: table-driven tests for the classification pipeline functions.
//
// Coverage:
//   - classifyByFilename: single keyword, multi-keyword non-determinism, short-keyword
//     false positives ("tax"→"taxi"), no-match.
//   - classifyByRelativePath: screenshot detection, music prefix, Download (no signal),
//     empty string, Samsung-style path (currently unmatched — drives EPIC-075 M3).
//   - classifyByIntentMetadata: known package, unknown package, known app_category,
//     unknown category, package-wins-over-category, empty request.
//   - classifyIntentProfile: cascade short-circuits at each stage, LLM fallback fires
//     only when all prior stages miss, all-miss with no hints returns "".
//
// Test seams:
//   - execContentClassify is overridden (same pattern as TestClassifyContentProfile)
//     to prevent real Haiku API calls.

import (
	"context"
	"testing"
)

// makeShareReq constructs a minimal ShareRequest covering all classification
// fields. Use named arguments to make test cases self-documenting.
func makeShareReq(pkg string, cat int, filename, relPath, subject, text string) *ShareRequest {
	return &ShareRequest{
		CallingPackage: pkg,
		AppCategory:    cat,
		Filename:       filename,
		RelativePath:   relPath,
		ExtraSubject:   subject,
		ExtraText:      text,
	}
}

// --- classifyByFilename ------------------------------------------------------

func TestClassifyByFilename(t *testing.T) {
	cases := []struct {
		name      string
		filename  string
		want      string   // expected result; "" = no match
		wantOneOf []string // when set, accept any value in the set (non-deterministic multi-keyword)
	}{
		// Single-keyword matches
		{"invoice", "invoice_2024.pdf", "finance", nil},
		{"receipt", "receipt_amazon.pdf", "finance", nil},
		{"statement", "bank_statement_jan.pdf", "finance", nil},
		{"payslip", "payslip_march.pdf", "finance", nil},
		{"resume", "resume_john.docx", "life", nil},
		{"recipe", "pasta_recipe.pdf", "dining", nil},
		{"menu", "restaurant_menu.pdf", "dining", nil},
		{"itinerary", "trip_itinerary.pdf", "travel", nil},
		{"boarding", "boarding_pass.pdf", "travel", nil},
		{"ticket", "concert_ticket.pdf", "travel", nil},

		// Case-insensitive matching
		{"uppercase Invoice", "INVOICE_2024.PDF", "finance", nil},
		{"mixed case", "My_Resume_2026.docx", "life", nil},

		// Short-keyword "cv" — direct match
		{"cv keyword direct match", "my_cv.pdf", "life", nil},

		// "cv" is NOT a substring of "recovery" (r-e-c-o-v-e-r-y has no "cv" run).
		// Confirmed: no false positive here.
		{"cv not in recovery — no match", "recovery_plan.docx", "", nil},

		// EPIC-075 M2: "tax" inside "taxi" — word-boundary matching prevents false positive.
		// After M2, "taxi_receipt_scan.jpg" should NOT match "tax" (it matches "receipt" instead).
		{"tax in taxi — no false positive after M2", "taxi_receipt_scan.jpg", "finance", nil}, // matches "receipt", not "tax"

		// EPIC-075 M2: "cv" must not match "recover" (no word boundary).
		{"cv not in recover", "recover_data.docx", "", nil},

		// EPIC-075 M2: "cv" with word boundary — matches "cv.pdf", "my_cv.pdf", "cv_john.pdf".
		{"cv word boundary match underscore", "cv_john.pdf", "life", nil},
		{"cv word boundary match dot", "cv.pdf", "life", nil},

		// EPIC-075 M2: Multi-keyword filename — ordered slice gives deterministic first-match.
		// Finance keywords appear before travel/life/dining in the slice, so finance wins.
		{
			name:     "invoice and receipt — finance wins (first match)",
			filename: "invoice_receipt_2024.pdf",
			want:     "finance",
		},
		{
			name:     "itinerary and ticket — itinerary wins (first in slice)",
			filename: "itinerary_ticket.pdf",
			want:     "travel",
		},
		{
			name:     "resume and recipe — resume wins (appears first in slice)",
			filename: "resume_recipe_combo.pdf",
			want:     "life",
		},

		// No match
		{"no match", "photo_2024.jpg", "", nil},
		{"no match — generic document", "document.pdf", "", nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyByFilename(c.filename)

			if len(c.wantOneOf) > 0 {
				// Non-deterministic case: accept any member of the set.
				// This documents the pre-EPIC-075 M2 map-iteration non-determinism.
				// After EPIC-075 M2 replaces filenameKeywords map with an ordered slice,
				// these cases should be updated to assert a single deterministic winner.
				t.Logf("classifyByFilename(%q) = %q (non-deterministic; any of %v acceptable)", c.filename, got, c.wantOneOf)
				found := false
				for _, acceptable := range c.wantOneOf {
					if got == acceptable {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("classifyByFilename(%q) = %q, not in acceptable set %v", c.filename, got, c.wantOneOf)
				}
				return
			}

			if got != c.want {
				t.Errorf("classifyByFilename(%q) = %q, want %q", c.filename, got, c.want)
			}
		})
	}
}

// --- classifyByRelativePath --------------------------------------------------

func TestClassifyByRelativePath(t *testing.T) {
	cases := []struct {
		name           string
		relPath        string
		wantProfile    string
		wantScreenshot bool
	}{
		// Screenshot detection
		{"DCIM/Screenshots", "DCIM/Screenshots/shot.jpg", "", true},
		{"Screenshots prefix", "Screenshots/img.png", "", true},
		{"DCIM/Screenshots nested", "DCIM/Screenshots/sub/shot.jpg", "", true},

		// Music / recordings
		{"Music prefix", "Music/track.mp3", "music", false},
		{"Recordings prefix", "Recordings/memo.mp3", "music", false},

		// Too generic — no classification signal
		{"Download prefix — no signal", "Download/file.pdf", "", false},
		{"Documents prefix — no signal", "Documents/report.docx", "", false},

		// Empty / unrecognized
		{"empty string", "", "", false},
		{"DCIM root — no match", "DCIM/Camera/photo.jpg", "", false},

		// EPIC-075 M5: Samsung and Xiaomi screenshot paths now matched.
		{"Samsung Pictures/Screenshots", "Pictures/Screenshots/shot.jpg", "", true},
		{"Samsung nested", "Pictures/Screenshots/2026/shot.jpg", "", true},
		{"Xiaomi Screencap", "Screencap/shot.jpg", "", true},
		{"Xiaomi Screencap nested", "Screencap/2026-04/shot.jpg", "", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotProfile, gotScreenshot := classifyByRelativePath(c.relPath)
			if gotProfile != c.wantProfile {
				t.Errorf("classifyByRelativePath(%q) profile = %q, want %q", c.relPath, gotProfile, c.wantProfile)
			}
			if gotScreenshot != c.wantScreenshot {
				t.Errorf("classifyByRelativePath(%q) isScreenshot = %v, want %v", c.relPath, gotScreenshot, c.wantScreenshot)
			}
		})
	}
}

// --- classifyBySubjectKeywords -----------------------------------------------

func TestClassifyBySubjectKeywords(t *testing.T) {
	cases := []struct {
		name    string
		subject string
		want    string
	}{
		// Finance
		{"portfolio mention", "Check out my investment portfolio performance", "finance"},
		{"stock ticker", "AAPL stock is up 3% today", "finance"},
		{"invest keyword", "Best ways to invest your savings in 2026", "finance"},
		{"invoice in subject", "Invoice #1234 from Acme Corp", "finance"},

		// Dining
		{"recipe title", "Easy pasta recipe for weeknights", "dining"},
		{"restaurant name", "This restaurant has amazing reviews", "dining"},
		{"menu item", "Check out the new menu at the local bistro", "dining"},

		// Travel
		{"flight info", "Your flight departs at 6am from SFO", "travel"},
		{"hotel booking", "Hotel reservation confirmed for 3 nights", "travel"},
		{"itinerary", "Trip itinerary: Day 1 – Rome", "travel"},

		// Music
		{"album release", "New album drops Friday", "music"},
		{"playlist share", "My running playlist for the weekend", "music"},
		{"track name", "This track is fire", "music"},

		// Case insensitive
		{"uppercase RECIPE", "RECIPE: How to make sourdough", "dining"},
		{"mixed case Flight", "Flight SFO→JFK confirmed", "travel"},

		// No match
		{"generic subject", "FYI check this out", ""},
		{"empty subject", "", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyBySubjectKeywords(c.subject)
			if got != c.want {
				t.Errorf("classifyBySubjectKeywords(%q) = %q, want %q", c.subject, got, c.want)
			}
		})
	}
}

// --- compositeProfileOverride ------------------------------------------------

func TestCompositeProfileOverride(t *testing.T) {
	cases := []struct {
		name    string
		pkg     string
		subject string
		text    string
		want    string
	}{
		// Maps + restaurant keywords → dining override
		{"maps restaurant subject", "com.google.android.apps.maps", "Great restaurant nearby", "", "dining"},
		{"maps cafe text", "com.google.android.apps.maps", "", "This cafe has amazing coffee", "dining"},
		{"maps food keyword", "com.google.android.apps.maps", "Best food in the area", "", "dining"},
		{"maps menu keyword", "com.google.android.apps.maps", "Lunch menu available", "", "dining"},

		// Maps without restaurant signal → no override (falls back to travel)
		{"maps no signal", "com.google.android.apps.maps", "Directions to the airport", "", ""},
		{"maps empty subject/text", "com.google.android.apps.maps", "", "", ""},

		// Other packages → no override
		{"spotify no override", "com.spotify.music", "restaurant playlist", "", ""},
		{"instagram no override", "com.instagram.android", "Best restaurant in NYC", "", ""},
		{"empty package", "", "restaurant review", "", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := compositeProfileOverride(c.pkg, c.subject, c.text)
			if got != c.want {
				t.Errorf("compositeProfileOverride(%q, %q, %q) = %q, want %q", c.pkg, c.subject, c.text, got, c.want)
			}
		})
	}
}

// --- classifyByIntentMetadata (M4 MIME + composite) --------------------------

func TestClassifyByIntentMetadata_MIME(t *testing.T) {
	cases := []struct {
		name     string
		mimeType string
		want     string
	}{
		{"excel xls → finance", "application/vnd.ms-excel", "finance"},
		{"excel xlsx → finance", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "finance"},
		{"vcard → life", "text/x-vcard", "life"},
		{"vcard alt → life", "text/vcard", "life"},
		{"pdf — no mapping → empty", "application/pdf", ""},
		{"jpeg — no mapping → empty", "image/jpeg", ""},
		{"empty mime → empty", "", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := &ShareRequest{MimeType: c.mimeType}
			got := classifyByIntentMetadata(req)
			if got != c.want {
				t.Errorf("classifyByIntentMetadata(mime=%q) = %q, want %q", c.mimeType, got, c.want)
			}
		})
	}
}

// --- classifyByIntentMetadata ------------------------------------------------

func TestClassifyByIntentMetadata(t *testing.T) {
	cases := []struct {
		name string
		req  *ShareRequest
		want string
	}{
		// Known package names → high-confidence classification
		{"spotify → music", makeShareReq("com.spotify.music", 0, "", "", "", ""), "music"},
		{"youtube → eng", makeShareReq("com.google.android.youtube", 0, "", "", "", ""), "eng"},

		// Unknown package — falls through to app category
		{"unknown package → empty", makeShareReq("com.unknown.app", 0, "", "", "", ""), ""},

		// Known app categories (no package)
		{"category 1 (audio) → music", makeShareReq("", 1, "", "", "", ""), "music"},
		{"category 4 (social) → life", makeShareReq("", 4, "", "", "", ""), "life"},
		{"category 5 (news) → eng", makeShareReq("", 5, "", "", "", ""), "eng"},
		{"category 6 (maps) → travel", makeShareReq("", 6, "", "", "", ""), "travel"},

		// EPIC-081 M3: CATEGORY_IMAGE removed — image shares routed via type-based logic.
		{"category 3 (image) → empty", makeShareReq("", 3, "", "", "", ""), ""},
		// Unknown / unmapped categories
		{"unknown category → empty", makeShareReq("", 99, "", "", "", ""), ""},

		// Package takes precedence over category (code checks package first)
		{"known package overrides category", makeShareReq("com.spotify.music", 6, "", "", "", ""), "music"},

		// EPIC-075 M4: multi-topic packages removed from packageProfileMap — fall through
		{"instagram → empty (multi-topic, removed M4)", makeShareReq("com.instagram.android", 0, "", "", "", ""), ""},
		{"reddit → empty (multi-topic, removed M4)", makeShareReq("com.reddit.frontpage", 0, "", "", "", ""), ""},

		// Completely empty request
		{"empty request → empty", makeShareReq("", 0, "", "", "", ""), ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyByIntentMetadata(c.req)
			if got != c.want {
				t.Errorf("classifyByIntentMetadata() = %q, want %q", got, c.want)
			}
		})
	}
}

// --- detectScreenshot (EPIC-077 M4) ------------------------------------------

func TestDetectScreenshot(t *testing.T) {
	cases := []struct {
		name           string
		relPath        string
		wantScreenshot bool
	}{
		{"DCIM/Screenshots → screenshot", "DCIM/Screenshots/shot.jpg", true},
		{"Pictures/Screenshots → screenshot", "Pictures/Screenshots/shot.jpg", true},
		{"Screencap → screenshot", "Screencap/2026/shot.jpg", true},
		{"Music/ → not screenshot", "Music/track.mp3", false},
		{"Download/ → not screenshot", "Download/file.pdf", false},
		{"empty → not screenshot", "", false},
		{"DCIM/Camera → not screenshot", "DCIM/Camera/photo.jpg", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := &ShareRequest{RelativePath: c.relPath, IsScreenshot: false}
			detectScreenshot(req)
			if req.IsScreenshot != c.wantScreenshot {
				t.Errorf("detectScreenshot(%q): IsScreenshot=%v, want %v", c.relPath, req.IsScreenshot, c.wantScreenshot)
			}
		})
	}
}

// TestDetectScreenshot_PreExisting verifies detectScreenshot does not clear
// an already-set IsScreenshot flag (e.g. set by Android client).
func TestDetectScreenshot_PreExisting(t *testing.T) {
	req := &ShareRequest{IsScreenshot: true, RelativePath: "Music/track.mp3"}
	detectScreenshot(req)
	if !req.IsScreenshot {
		t.Error("detectScreenshot must not clear pre-set IsScreenshot=true")
	}
}

// --- classifyShareRequest (EPIC-077 M4) --------------------------------------

func TestClassifyShareRequest_Cascade(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name        string
		req         *ShareRequest
		llmReturn   string
		wantProfile string
		wantSource  string
		wantLLM     bool
	}{
		{
			name:        "stage 1: known package wins",
			req:         makeShareReq("com.spotify.music", 0, "", "", "", ""),
			wantProfile: "music",
			wantSource:  "intent_metadata",
			wantLLM:     false,
		},
		{
			name:        "stage 2: filename keyword wins",
			req:         makeShareReq("", 0, "invoice_2024.pdf", "", "", ""),
			wantProfile: "finance",
			wantSource:  "filename",
			wantLLM:     false,
		},
		{
			name:        "stage 3: subject keyword wins",
			req:         makeShareReq("", 0, "", "", "Best flight deals this summer", ""),
			wantProfile: "travel",
			wantSource:  "subject_keywords",
			wantLLM:     false,
		},
		{
			name:        "stage 4: relativePath wins",
			req:         makeShareReq("", 0, "", "Music/track.mp3", "", ""),
			wantProfile: "music",
			wantSource:  "relative_path",
			wantLLM:     false,
		},
		{
			name: "stage 5: URL domain positive match",
			req: &ShareRequest{
				URL:  "https://github.com/foo/bar",
				Type: "url",
			},
			wantProfile: "eng",
			wantSource:  "url_domain",
			wantLLM:     false,
		},
		{
			name: "stage 5: URL domain fallback returns sentinel",
			req: &ShareRequest{
				URL:  "https://unknown-site.example.com/page",
				Type: "url",
			},
			wantProfile: "eng",
			wantSource:  "url_domain_fallback",
			wantLLM:     false,
		},
		{
			name:        "stage 6: LLM hints fallback fires when no URL",
			req:         makeShareReq("", 0, "", "", "", "Best pasta I have ever had"),
			llmReturn:   "dining",
			wantProfile: "dining",
			wantSource:  "content_llm_hints",
			wantLLM:     true,
		},
		{
			name:        "all miss, no hints → empty",
			req:         makeShareReq("", 0, "", "", "", ""),
			wantProfile: "",
			wantSource:  "",
			wantLLM:     false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			llmCalls := 0
			backend := &funcScoringBackend{complete: func(_ context.Context, _, _ string) (string, error) {
				llmCalls++
				return c.llmReturn, nil
			}}

			gotProfile, gotSource := classifyShareRequest(ctx, backend, c.req)

			if gotProfile != c.wantProfile {
				t.Errorf("classifyShareRequest() profile=%q, want %q", gotProfile, c.wantProfile)
			}
			if gotSource != c.wantSource {
				t.Errorf("classifyShareRequest() source=%q, want %q", gotSource, c.wantSource)
			}
			if c.wantLLM && llmCalls == 0 {
				t.Error("expected LLM stub to be called, but it was not")
			}
			if !c.wantLLM && llmCalls > 0 {
				t.Errorf("expected LLM stub NOT called, but was called %d times", llmCalls)
			}
		})
	}
}

// TestClassifyShareRequest_ScreenshotNotConsumed verifies that the screenshot
// path (relativePath=DCIM/Screenshots) does NOT produce a profile from
// classifyShareRequest — screenshot detection is detectScreenshot's job, and
// classifyShareRequest skips screenshot entries in relativePathPrefixes.
func TestClassifyShareRequest_ScreenshotNotConsumed(t *testing.T) {
	ctx := context.Background()
	req := makeShareReq("", 0, "", "DCIM/Screenshots/shot.jpg", "", "")
	gotProfile, gotSource := classifyShareRequest(ctx, nil, req)
	// Screenshot path has isScreenshot=true, profile="" — classifyShareRequest
	// should not assign a profile from it.
	if gotProfile != "" || gotSource != "" {
		t.Errorf("classifyShareRequest() should not classify screenshot relPath; got profile=%q source=%q", gotProfile, gotSource)
	}
}

// --- classifyIntentProfile cascade -------------------------------------------

func TestClassifyIntentProfile_Cascade(t *testing.T) {
	ctx := context.Background()

	// Track how many times the LLM stub is called.
	type result struct {
		profile   string
		llmCalled int
	}

	cases := []struct {
		name       string
		req        *ShareRequest
		llmReturn  string // what the stub returns when called
		wantResult string
		wantLLM    bool // true if LLM should be called (stage 4)
	}{
		{
			name:       "stage 1 wins: known package",
			req:        makeShareReq("com.spotify.music", 0, "", "", "", ""),
			llmReturn:  "travel", // should not be called
			wantResult: "music",
			wantLLM:    false,
		},
		{
			name:       "stage 2 wins: filename keyword",
			req:        makeShareReq("", 0, "invoice_2024.pdf", "", "", ""),
			llmReturn:  "travel",
			wantResult: "finance",
			wantLLM:    false,
		},
		{
			name:       "stage 3 wins: subject keyword (EPIC-075 M3)",
			req:        makeShareReq("", 0, "", "", "Best flight deals for summer", ""),
			llmReturn:  "dining",
			wantResult: "travel",
			wantLLM:    false,
		},
		{
			name:       "stage 4 wins: relativePath music",
			req:        makeShareReq("", 0, "", "Music/track.mp3", "", ""),
			llmReturn:  "travel",
			wantResult: "music",
			wantLLM:    false,
		},
		{
			name:       "stage 5 fires: LLM classifies with ExtraText",
			req:        makeShareReq("", 0, "", "", "", "Best pasta I have ever had"),
			llmReturn:  "dining",
			wantResult: "dining",
			wantLLM:    true,
		},
		{
			name:       "stage 5 fires: LLM classifies with Filename hint",
			req:        makeShareReq("", 0, "unknown_document.bin", "", "", "Some random text"),
			llmReturn:  "eng",
			wantResult: "eng",
			wantLLM:    true,
		},
		{
			name:       "all miss, no hints — returns empty",
			req:        makeShareReq("", 0, "", "", "", ""),
			llmReturn:  "travel",
			wantResult: "",
			wantLLM:    false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			llmCallCount := 0
			backend := &funcScoringBackend{complete: func(_ context.Context, _, _ string) (string, error) {
				llmCallCount++
				return c.llmReturn, nil
			}}

			got := classifyIntentProfile(ctx, backend, c.req)

			if got != c.wantResult {
				t.Errorf("classifyIntentProfile() = %q, want %q", got, c.wantResult)
			}
			if c.wantLLM && llmCallCount == 0 {
				t.Errorf("expected LLM stub to be called, but it was not")
			}
			if !c.wantLLM && llmCallCount > 0 {
				t.Errorf("expected LLM stub NOT to be called, but was called %d times", llmCallCount)
			}
		})
	}
}

// --- classifySourceToStage (GAP-04 fix) --------------------------------------

// TestClassifySourceToStage verifies that every classify_source value produced
// by classifyShareRequestFast / classifyShareRequest maps to a non-"unknown"
// stage string, and that empty / unrecognized sources map to "unknown".
func TestClassifySourceToStage(t *testing.T) {
	cases := []struct {
		source    string
		wantStage string
	}{
		// Cascade stages 1-6.
		{"intent_metadata", "1"},
		{"filename", "2"},
		{"subject_keywords", "3"},
		{"relative_path", "4"},
		{"url_domain", "5"},
		{"url_domain_fallback", "5"},
		{"content", "6"},
		{"content_llm_hints", "6"},
		{"content_lm", "6"}, // audio pipeline variant

		// Special non-cascade sources.
		{"caller", "caller"},
		{"image_override", "image_override"},
		{"default_fallback", "default"},

		// GAP-04: empty and unknown must map to "unknown" (prior bug).
		{"", "unknown"},
		{"totally_new_source", "unknown"},
	}

	for _, c := range cases {
		t.Run(c.source, func(t *testing.T) {
			got := classifySourceToStage(c.source)
			if got != c.wantStage {
				t.Errorf("classifySourceToStage(%q) = %q, want %q", c.source, got, c.wantStage)
			}
		})
	}
}
