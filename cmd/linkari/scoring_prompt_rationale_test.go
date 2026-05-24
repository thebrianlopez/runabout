package main

import (
	"strings"
	"testing"
)

func TestUserRationalePrompt_CT1_IncludesLabeledSection(t *testing.T) {
	got := formatUserRationale("Useful only if it includes benchmarks.", "voice_transcript")
	for _, want := range []string{
		"User share-time rationale:",
		"- Source: voice_transcript",
		"- Text: Useful only if it includes benchmarks.",
		"stated evaluation intent",
		"Do not treat it as evidence",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatUserRationale missing %q in:\n%s", want, got)
		}
	}
}

func TestUserRationalePrompt_CT2_EmptyOmitted(t *testing.T) {
	if got := formatUserRationale("   ", "typed"); got != "" {
		t.Fatalf("empty rationale got %q, want empty", got)
	}
}

func TestUserRationalePrompt_CT3_CapsAtBudget(t *testing.T) {
	long := strings.Repeat("a", maxUserRationaleChars+50)
	got := formatUserRationale(long, "typed")
	if strings.Contains(got, strings.Repeat("a", maxUserRationaleChars+1)) {
		t.Fatalf("rationale was not capped")
	}
}
