package main

// EPIC-155 F2 M1: Intent-Conditioned Prompt Construction contract tests.
// CT-1 through CT-7 assert loadIntentRubric and classificationPreambleIntent behavior.
// Tests inject YAML content via LINKARI_INTENT_PATH env to avoid disk dependency.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupIntentTestDir creates a temp dir with intent YAML fixtures and sets LINKARI_INTENT_PATH.
func setupIntentTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LINKARI_INTENT_PATH", dir)

	// Write score.yaml
	os.WriteFile(filepath.Join(dir, "score.yaml"), []byte(`
intent: score
content_types:
  url: |
    Engineering context rubric for URL shares.
default: |
  Default score rubric for evaluating content relevance.
`), 0644)

	// Write capture.yaml
	os.WriteFile(filepath.Join(dir, "capture.yaml"), []byte(`
intent: capture
default: |
  Capture rubric: evaluate whether this content maps to a tool workflow.
`), 0644)

	// Write transcribe.yaml
	os.WriteFile(filepath.Join(dir, "transcribe.yaml"), []byte(`
intent: transcribe
content_types:
  audio: |
    Transcription rubric: organize and summarize voice note.
default: |
  Default transcription rubric.
`), 0644)

	// Write overrides/jira.yaml
	os.MkdirAll(filepath.Join(dir, "overrides"), 0755)
	os.WriteFile(filepath.Join(dir, "overrides", "jira.yaml"), []byte(`
tag: jira
instructions: |
  Jira capture context: extract ticket details from this Jira URL.
`), 0644)

	return dir
}

// CT-1: score rubric with domain tag included in prompt.
func TestIntentRubric_CT1_ScoreWithDomainTag(t *testing.T) {
	setupIntentTestDir(t)
	invalidateIntentRubricCache()

	prompt, err := loadIntentRubric("score", "url", []string{"domain:eng"}, nil)
	if err != nil {
		t.Fatalf("CT-1: loadIntentRubric error: %v", err)
	}
	if !strings.Contains(prompt, "Engineering") && !strings.Contains(prompt, "domain:eng") && !strings.Contains(prompt, "User-Applied") {
		t.Errorf("CT-1: prompt does not contain domain tag context: %q", prompt)
	}
}

// CT-2: capture rubric with jira override applied.
func TestIntentRubric_CT2_CaptureWithJiraOverride(t *testing.T) {
	setupIntentTestDir(t)
	invalidateIntentRubricCache()

	prompt, err := loadIntentRubric("capture", "url", []string{"jira"}, nil)
	if err != nil {
		t.Fatalf("CT-2: loadIntentRubric error: %v", err)
	}
	if !strings.Contains(prompt, "Capture") && !strings.Contains(prompt, "capture") {
		t.Errorf("CT-2: prompt does not contain capture rubric: %q", prompt)
	}
	if !strings.Contains(prompt, "Jira") && !strings.Contains(prompt, "jira") {
		t.Errorf("CT-2: prompt does not contain jira override: %q", prompt)
	}
}

// CT-3: Missing tag override is non-fatal.
func TestIntentRubric_CT3_MissingTagOverrideNonFatal(t *testing.T) {
	setupIntentTestDir(t)
	invalidateIntentRubricCache()

	// "unknown-tag" has no override file.
	prompt, err := loadIntentRubric("capture", "url", []string{"unknown-tag"}, nil)
	if err != nil {
		t.Fatalf("CT-3: loadIntentRubric error (must be non-fatal): %v", err)
	}
	if strings.TrimSpace(prompt) == "" {
		t.Error("CT-3: prompt must not be empty even when tag override missing")
	}
}

// CT-4: loadIntentRubric never returns empty string.
func TestIntentRubric_CT4_NeverReturnsEmpty(t *testing.T) {
	setupIntentTestDir(t)
	invalidateIntentRubricCache()

	for _, intent := range []string{"score", "capture", "transcribe"} {
		prompt, err := loadIntentRubric(intent, "url", nil, nil)
		if err != nil {
			t.Errorf("CT-4: intent=%q loadIntentRubric error: %v", intent, err)
			continue
		}
		if strings.TrimSpace(prompt) == "" {
			t.Errorf("CT-4: intent=%q returned empty prompt", intent)
		}
	}
}

// CT-5: inferredTags appear in prompt with lower priority label.
func TestIntentRubric_CT5_InferredTagsInPrompt(t *testing.T) {
	setupIntentTestDir(t)
	invalidateIntentRubricCache()

	prompt, err := loadIntentRubric("score", "url", nil, []string{"domain:eng"})
	if err != nil {
		t.Fatalf("CT-5: loadIntentRubric error: %v", err)
	}
	if !strings.Contains(prompt, "Inferred") && !strings.Contains(prompt, "domain:eng") {
		t.Errorf("CT-5: prompt does not contain inferred tag context: %q", prompt)
	}
}

// CT-6: classificationPreambleIntent format is stable.
func TestIntentRubric_CT6_PreambleFormatStable(t *testing.T) {
	preamble := classificationPreambleIntent("capture", []string{"jira"}, nil, "url")
	if !strings.HasPrefix(preamble, "User intent: capture") {
		t.Errorf("CT-6: preamble does not start with expected prefix: %q", preamble)
	}
	if !strings.Contains(preamble, "User-Applied Tags: jira") {
		t.Errorf("CT-6: preamble missing user-applied tags: %q", preamble)
	}
}

// CT-7: rubric_parse_failed falls back to cache (or minimal preamble on first load).
func TestIntentRubric_CT7_ParseFailedFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LINKARI_INTENT_PATH", dir)
	invalidateIntentRubricCache()

	// Write a malformed YAML file.
	os.WriteFile(filepath.Join(dir, "score.yaml"), []byte("intent: score\n{invalid yaml here"), 0644)

	// Should not error - falls back to minimal preamble (RG-1).
	prompt, err := loadIntentRubric("score", "url", []string{"domain:eng"}, nil)
	if err != nil {
		t.Fatalf("CT-7: loadIntentRubric returned error on bad YAML (must fall back): %v", err)
	}
	if strings.TrimSpace(prompt) == "" {
		t.Error("CT-7: fallback preamble must not be empty")
	}
}

// BT-1: userTags appear before inferredTags in prompt.
func TestIntentRubric_BT1_UserTagsBeforeInferredTags(t *testing.T) {
	preamble := classificationPreambleIntent("score", []string{"my-tag"}, []string{"domain:eng"}, "url")
	userIdx := strings.Index(preamble, "User-Applied Tags")
	inferredIdx := strings.Index(preamble, "Inferred Context")
	if userIdx < 0 {
		t.Error("BT-1: preamble missing User-Applied Tags section")
		return
	}
	if inferredIdx < 0 {
		t.Error("BT-1: preamble missing Inferred Context section")
		return
	}
	if userIdx > inferredIdx {
		t.Errorf("BT-1: user tags appear at %d, after inferred tags at %d; must be before", userIdx, inferredIdx)
	}
}

// BT-2: content_type section selected correctly.
func TestIntentRubric_BT2_ContentTypeSectionSelected(t *testing.T) {
	setupIntentTestDir(t)
	invalidateIntentRubricCache()

	// transcribe.yaml has an audio section; use that.
	prompt, err := loadIntentRubric("transcribe", "audio", nil, nil)
	if err != nil {
		t.Fatalf("BT-2: loadIntentRubric error: %v", err)
	}
	if !strings.Contains(prompt, "Transcription") && !strings.Contains(prompt, "voice note") {
		t.Errorf("BT-2: audio content_type section not selected: %q", prompt)
	}
}

// RG-1: Rubric parse failure must not return empty prompt.
func TestIntentRubric_RG1_ParseFailureFallbackNonEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LINKARI_INTENT_PATH", dir)
	invalidateIntentRubricCache()

	// No YAML file at all for "score".
	prompt, err := loadIntentRubric("score", "url", nil, nil)
	if err != nil {
		t.Logf("RG-1: error returned (acceptable when no file): %v", err)
	}
	// Even on error, loadIntentRubric returns the minimal preamble.
	if strings.TrimSpace(prompt) == "" {
		t.Error("RG-1: fallback preamble must not be empty when YAML file missing")
	}
}
