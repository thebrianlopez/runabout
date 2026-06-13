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

func TestIsRationaleQuestion(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"is this the same author?", true},
		{"how do they make money?", true},
		{"what is the main argument", true},
		{"lets clone this into a prd", false},
		{"agentic-dev", false},
		{"", false},
		{"check our transcripts to see if we already scored this", false},
		{"can we use this in our stack?", true},
	}
	for _, c := range cases {
		got := isRationaleQuestion(c.input)
		if got != c.want {
			t.Errorf("isRationaleQuestion(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}
