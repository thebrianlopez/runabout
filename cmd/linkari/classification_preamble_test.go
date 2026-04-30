package main

// EPIC-015 M1: Contract tests for classificationPreamble with all four ContentType values.
//
// CT-1: ContentTypePlain output is byte-identical to what the current signature produces.
// CT-2: ContentTypeMarkdown output contains a markdown format hint.
// CT-3: ContentTypeADF output contains a structured document hint.
// CT-4: ContentTypeJSON output contains a structured data hint.
// CT-5: empty source defaults to "url" (existing behavior preserved).
//
// These tests are written before the implementation (M1 gate for M2).
// CT-1 through CT-5 must all pass after M2 lands.

import (
	"fmt"
	"strings"
	"testing"
)

// plainPreamble returns the byte-identical output of the pre-EPIC-015
// classificationPreamble for the given inputs. This is the regression
// baseline for CT-1.
func plainPreamble(profile, rawURL, source string) string {
	if source == "" {
		source = "url"
	}
	return fmt.Sprintf(
		"[Auto-classified profile: %s (source: %s, URL: %s)]\n"+
			"Score this content using the %s profile rubric.\n\n",
		profile, source, rawURL, profile,
	)
}

func TestClassificationPreambleContentType(t *testing.T) {
	const (
		profile = "engineering"
		rawURL  = "https://github.com/trycua/cua"
		source  = "domain"
	)

	t.Run("CT-1 ContentTypePlain byte-identical to baseline", func(t *testing.T) {
		want := plainPreamble(profile, rawURL, source)
		got := classificationPreamble(profile, rawURL, source, ContentTypePlain)
		if got != want {
			t.Errorf("ContentTypePlain regression:\nwant: %q\n got: %q", want, got)
		}
	})

	t.Run("CT-2 ContentTypeMarkdown contains markdown hint", func(t *testing.T) {
		got := classificationPreamble(profile, rawURL, source, ContentTypeMarkdown)
		if !strings.Contains(got, "markdown") {
			t.Errorf("ContentTypeMarkdown preamble missing 'markdown' hint:\n%s", got)
		}
	})

	t.Run("CT-3 ContentTypeADF contains structured document hint", func(t *testing.T) {
		got := classificationPreamble(profile, rawURL, source, ContentTypeADF)
		if !strings.Contains(got, "Confluence") {
			t.Errorf("ContentTypeADF preamble missing 'Confluence' hint:\n%s", got)
		}
	})

	t.Run("CT-4 ContentTypeJSON contains structured data hint", func(t *testing.T) {
		got := classificationPreamble(profile, rawURL, source, ContentTypeJSON)
		if !strings.Contains(got, "JSON") && !strings.Contains(got, "json") {
			t.Errorf("ContentTypeJSON preamble missing JSON hint:\n%s", got)
		}
	})

	t.Run("CT-5 empty source defaults to url", func(t *testing.T) {
		got := classificationPreamble(profile, rawURL, "", ContentTypePlain)
		want := plainPreamble(profile, rawURL, "")
		if got != want {
			t.Errorf("empty source: got %q, want %q", got, want)
		}
		if !strings.Contains(got, "source: url") {
			t.Errorf("empty source did not default to 'url': %q", got)
		}
	})
}
