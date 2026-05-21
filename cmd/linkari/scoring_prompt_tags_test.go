package main

// EPIC-127 F5 CT-1 through CT-4: pure unit tests for formatUserTags.
// No DB, no network, no subprocess.

import (
	"strings"
	"testing"
)

// CT-1: formatUserTags(nil) returns "".
func TestF5_CT1_FormatUserTags_Nil_ReturnsEmpty(t *testing.T) {
	if got := formatUserTags(nil); got != "" {
		t.Errorf("formatUserTags(nil) = %q; want \"\"", got)
	}
}

// CT-2: formatUserTags([]string{}) returns "".
func TestF5_CT2_FormatUserTags_Empty_ReturnsEmpty(t *testing.T) {
	if got := formatUserTags([]string{}); got != "" {
		t.Errorf("formatUserTags([]) = %q; want \"\"", got)
	}
}

// CT-3: formatUserTags(["urgent", "reference"]) contains the expected section header.
func TestF5_CT3_FormatUserTags_NonEmpty_ContainsSection(t *testing.T) {
	got := formatUserTags([]string{"urgent", "reference"})
	if !strings.Contains(got, "User-Applied Tags: urgent, reference") {
		t.Errorf("formatUserTags output missing expected section; got %q", got)
	}
	if !strings.Contains(got, "deliberate intent") {
		t.Errorf("formatUserTags output missing intent framing; got %q", got)
	}
}

// CT-4: formatUserTags with >10 tags caps at 10 (t11 absent, t1 present).
func TestF5_CT4_FormatUserTags_CapAt10(t *testing.T) {
	tags := []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9", "t10", "t11"}
	got := formatUserTags(tags)
	if strings.Contains(got, "t11") {
		t.Errorf("formatUserTags should cap at 10 tags; t11 must not appear; got %q", got)
	}
	if !strings.Contains(got, "t1") {
		t.Errorf("formatUserTags missing first tag t1; got %q", got)
	}
	if !strings.Contains(got, "t10") {
		t.Errorf("formatUserTags missing tenth tag t10; got %q", got)
	}
}
