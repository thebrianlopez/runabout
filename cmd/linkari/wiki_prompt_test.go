package main

// EPIC-180 M3: wiki_prompt contract tests
//
// CT-6 is the critical regression guard  -  written before any prompt construction
// changes and must pass both before and after M3 wiring in scoreAsync.
//
// CT-1: approxTokens returns len(s)/4
// CT-2: approxTokens empty string returns 0
// CT-3: trimToTokenBudget  -  content under budget returned unchanged
// CT-4: trimToTokenBudget  -  content over budget tail-trimmed to maxTokens*4 chars
// CT-5: buildWikiContextBlock  -  missing file returns error
// CT-6: buildScoringPrompt  -  empty wikiBlock produces byte-identical baseline (REGRESSION GUARD)
// CT-7: buildScoringPrompt  -  non-empty wikiBlock appended to prompt
// CT-8: buildWikiContextBlock  -  populated file returns formatted block containing content
// CT-9: buildWikiContextBlock  -  content over budget is trimmed; result contains only tail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- CT-1: approxTokens basic ---

func TestWikiPrompt_CT1_ApproxTokens_Basic(t *testing.T) {
	s := "hello" // len=5, 5/4=1
	if got := approxTokens(s); got != 1 {
		t.Errorf("CT-1: approxTokens(%q) = %d, want 1", s, got)
	}
	s2 := "1234567890ab" // len=12, 12/4=3
	if got := approxTokens(s2); got != 3 {
		t.Errorf("CT-1: approxTokens(%q) = %d, want 3", s2, got)
	}
}

// --- CT-2: approxTokens empty ---

func TestWikiPrompt_CT2_ApproxTokens_Empty(t *testing.T) {
	if got := approxTokens(""); got != 0 {
		t.Errorf("CT-2: approxTokens(\"\") = %d, want 0", got)
	}
}

// --- CT-3: trimToTokenBudget under budget ---

func TestWikiPrompt_CT3_TrimToTokenBudget_UnderBudget_Unchanged(t *testing.T) {
	content := "short content" // len=13, tokens=3
	got := trimToTokenBudget(content, 100)
	if got != content {
		t.Errorf("CT-3: content under budget should be unchanged; got %q, want %q", got, content)
	}
}

// --- CT-4: trimToTokenBudget over budget keeps tail ---

func TestWikiPrompt_CT4_TrimToTokenBudget_OverBudget_KeepsTail(t *testing.T) {
	// 20 chars: "AAAABBBBCCCCDDDDEEEE"
	content := "AAAABBBBCCCCDDDDEEEE"
	// maxTokens=2 → maxChars=8 → keep last 8 chars = "DDDDEEEE"
	got := trimToTokenBudget(content, 2)
	want := "DDDDEEEE"
	if got != want {
		t.Errorf("CT-4: trimToTokenBudget(20-char, 2) = %q, want %q", got, want)
	}
}

// --- CT-5: buildWikiContextBlock missing file returns error ---

func TestWikiPrompt_CT5_BuildWikiContextBlock_MissingFile_Error(t *testing.T) {
	_, err := buildWikiContextBlock("/tmp/linkari-nonexistent-wiki-ct5/index.md", 500)
	if err == nil {
		t.Error("CT-5: expected error for missing file, got nil")
	}
}

// --- CT-6: REGRESSION GUARD  -  empty wikiBlock produces byte-identical prompt ---

func TestWikiPrompt_CT6_BuildScoringPrompt_NoWikiBlock_IdenticalToBaseline(t *testing.T) {
	baseline := "Score this content using the eng profile rubric.\n\nSome rubric content here."
	got := buildScoringPrompt(baseline, "")
	if got != baseline {
		t.Errorf("CT-6: buildScoringPrompt(baseline, \"\") changed the prompt\ngot:  %q\nwant: %q", got, baseline)
	}
}

// --- CT-7: buildScoringPrompt with non-empty wikiBlock appends it ---

func TestWikiPrompt_CT7_BuildScoringPrompt_WithWikiBlock_Appended(t *testing.T) {
	baseline := "Score this content using the eng profile rubric.\n"
	wikiBlock := "\n\n## Wiki Topic Context\n\nsome topic content\n"
	got := buildScoringPrompt(baseline, wikiBlock)
	if !strings.HasPrefix(got, baseline) {
		t.Error("CT-7: result should start with baseline prompt")
	}
	if !strings.Contains(got, wikiBlock) {
		t.Error("CT-7: result should contain wikiBlock")
	}
	if got == baseline {
		t.Error("CT-7: result should differ from baseline when wikiBlock non-empty")
	}
}

// --- CT-8: buildWikiContextBlock with content returns formatted block ---

func TestWikiPrompt_CT8_BuildWikiContextBlock_Content_FormattedBlock(t *testing.T) {
	dir := t.TempDir()
	indexPath := filepath.Join(dir, "_index.md")
	content := "# Golang\n\nKey topic: interfaces, goroutines, channels.\n"
	if err := os.WriteFile(indexPath, []byte(content), 0o600); err != nil {
		t.Fatalf("CT-8: write index: %v", err)
	}
	block, err := buildWikiContextBlock(indexPath, 500)
	if err != nil {
		t.Fatalf("CT-8: unexpected error: %v", err)
	}
	if block == "" {
		t.Fatal("CT-8: expected non-empty block")
	}
	if !strings.Contains(block, "Wiki Topic Context") {
		t.Error("CT-8: block should contain 'Wiki Topic Context' header")
	}
	if !strings.Contains(block, "interfaces") {
		t.Error("CT-8: block should contain the index file content")
	}
	if !strings.Contains(block, "gap") {
		t.Error("CT-8: block should contain gap rubric extension")
	}
}

// --- CT-9: buildWikiContextBlock large content is tail-trimmed ---

func TestWikiPrompt_CT9_BuildWikiContextBlock_LargeContent_Trimmed(t *testing.T) {
	dir := t.TempDir()
	indexPath := filepath.Join(dir, "_index.md")
	// Build 200-char string: first half "A"s, second half "B"s
	content := strings.Repeat("A", 100) + strings.Repeat("B", 100)
	if err := os.WriteFile(indexPath, []byte(content), 0o600); err != nil {
		t.Fatalf("CT-9: write index: %v", err)
	}
	// maxTokens=2 → maxChars=8 → last 8 chars from "BBBB...BBB" = "BBBBBBBB"
	block, err := buildWikiContextBlock(indexPath, 2)
	if err != nil {
		t.Fatalf("CT-9: unexpected error: %v", err)
	}
	if strings.Contains(block, "A") {
		t.Error("CT-9: trimmed block should not contain head content (A chars)")
	}
	if !strings.Contains(block, "B") {
		t.Error("CT-9: trimmed block should contain tail content (B chars)")
	}
}
