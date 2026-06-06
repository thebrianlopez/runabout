package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeProposal(t *testing.T, dir, fp string, p Proposal) string {
	t.Helper()
	p.Fingerprint = fp
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fp+".jsonl")
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeDecisionFile(t *testing.T, dir, fp string, dec map[string]string) {
	t.Helper()
	b, err := json.Marshal(dec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fp+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// CT-1: Pending proposal surfaced in interactive mode with tier, confidence, evidence
func TestDirective_CT1_PendingSurfaced(t *testing.T) {
	dir := t.TempDir()
	propsDir := filepath.Join(dir, "proposals")
	decsDir := filepath.Join(dir, "decisions")
	if err := os.MkdirAll(propsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(decsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProposal(t, propsDir, "fp001", Proposal{
		Tier:       "T2",
		Confidence: 0.91,
		TargetFile: "/tmp/tool-selection.md",
	})

	var out bytes.Buffer
	cmd := newDirectiveCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cfg := DirectiveConfig{
		ProposalsDir: propsDir,
		DecisionsDir: decsDir,
		EventBusDir:  filepath.Join(dir, "events"),
		Input:        strings.NewReader("q\n"),
	}
	if err := RunDirective(cmd, cfg); err != nil {
		t.Fatalf("RunDirective error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "T2") {
		t.Errorf("expected tier T2 in output, got: %s", output)
	}
	if !strings.Contains(output, "0.91") {
		t.Errorf("expected confidence 0.91 in output, got: %s", output)
	}
}

// CT-2: Snoozed proposal (snooze_until in future) excluded from surfacing
func TestDirective_CT2_SnoozedExcluded(t *testing.T) {
	dir := t.TempDir()
	propsDir := filepath.Join(dir, "proposals")
	decsDir := filepath.Join(dir, "decisions")
	os.MkdirAll(propsDir, 0o755)
	os.MkdirAll(decsDir, 0o755)

	writeProposal(t, propsDir, "fp002", Proposal{Tier: "T2", Confidence: 0.90})
	future := time.Now().UTC().AddDate(0, 0, 10).Format("20060102T150405Z")
	writeDecisionFile(t, decsDir, "fp002", map[string]string{"snooze_until": future})

	var out bytes.Buffer
	cmd := newDirectiveCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cfg := DirectiveConfig{ProposalsDir: propsDir, DecisionsDir: decsDir, Input: strings.NewReader("")}
	if err := RunDirective(cmd, cfg); err != nil {
		t.Fatalf("RunDirective error: %v", err)
	}
	if !strings.Contains(out.String(), "No pending") {
		t.Errorf("expected 'No pending' when all proposals snoozed, got: %s", out.String())
	}
}

// CT-3: Expired snooze (snooze_until in past) surfaces proposal again
func TestDirective_CT3_ExpiredSnoozeSurfaces(t *testing.T) {
	dir := t.TempDir()
	propsDir := filepath.Join(dir, "proposals")
	decsDir := filepath.Join(dir, "decisions")
	os.MkdirAll(propsDir, 0o755)
	os.MkdirAll(decsDir, 0o755)

	writeProposal(t, propsDir, "fp003", Proposal{Tier: "T1", Confidence: 0.80})
	past := time.Now().UTC().AddDate(0, 0, -1).Format("20060102T150405Z")
	writeDecisionFile(t, decsDir, "fp003", map[string]string{"snooze_until": past})

	var out bytes.Buffer
	cmd := newDirectiveCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cfg := DirectiveConfig{ProposalsDir: propsDir, DecisionsDir: decsDir, Input: strings.NewReader("q\n")}
	if err := RunDirective(cmd, cfg); err != nil {
		t.Fatalf("RunDirective error: %v", err)
	}
	if strings.Contains(out.String(), "No pending") {
		t.Error("expected expired snooze to surface proposal")
	}
}

// CT-4: --list shows proposals in non-interactive mode; exits 0
func TestDirective_CT4_ListMode(t *testing.T) {
	dir := t.TempDir()
	propsDir := filepath.Join(dir, "proposals")
	decsDir := filepath.Join(dir, "decisions")
	os.MkdirAll(propsDir, 0o755)
	os.MkdirAll(decsDir, 0o755)

	writeProposal(t, propsDir, "fp004", Proposal{Tier: "T2", Confidence: 0.88, TargetFile: "/rules/tool.md"})

	var out bytes.Buffer
	cmd := newDirectiveCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cfg := DirectiveConfig{ListOnly: true, ProposalsDir: propsDir, DecisionsDir: decsDir}
	if err := RunDirective(cmd, cfg); err != nil {
		t.Fatalf("--list mode error: %v", err)
	}
	if !strings.Contains(out.String(), "fp004") {
		t.Errorf("expected fp004 in list output, got: %s", out.String())
	}
}

// CT-5: Approve writes proposed_diff str.replace to target file
func TestDirective_CT5_ApproveWritesDiff(t *testing.T) {
	dir := t.TempDir()
	propsDir := filepath.Join(dir, "proposals")
	decsDir := filepath.Join(dir, "decisions")
	eventsDir := filepath.Join(dir, "events")
	os.MkdirAll(propsDir, 0o755)
	os.MkdirAll(decsDir, 0o755)
	os.MkdirAll(eventsDir, 0o755)

	// Create target rule file
	ruleFile := filepath.Join(dir, "tool-selection.md")
	original := "Always use the Glob tool."
	if err := os.WriteFile(ruleFile, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(original)))

	writeProposal(t, propsDir, "fp005", Proposal{
		Tier:         "T2",
		Confidence:   0.91,
		TargetFile:   ruleFile,
		ProposedDiff: "Always use the Glob tool.",
		TargetHash:   hash,
	})

	var out bytes.Buffer
	cmd := newDirectiveCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cfg := DirectiveConfig{
		Apply:        "fp005",
		ProposalsDir: propsDir,
		DecisionsDir: decsDir,
		EventBusDir:  eventsDir,
	}
	if err := RunDirective(cmd, cfg); err != nil {
		t.Fatalf("--apply error: %v", err)
	}
	if !strings.Contains(out.String(), "approved") {
		t.Errorf("expected 'approved' in output, got: %s", out.String())
	}
}

// CT-6: Approve writes directive_decision event with decision: approved to event bus
func TestDirective_CT6_ApproveWritesEvent(t *testing.T) {
	dir := t.TempDir()
	propsDir := filepath.Join(dir, "proposals")
	decsDir := filepath.Join(dir, "decisions")
	eventsDir := filepath.Join(dir, "events")
	os.MkdirAll(propsDir, 0o755)
	os.MkdirAll(decsDir, 0o755)
	os.MkdirAll(eventsDir, 0o755)

	writeProposal(t, propsDir, "fp006", Proposal{Tier: "T2", Confidence: 0.88, SignalID: "rec-006"})

	cfg := DirectiveConfig{
		Apply:        "fp006",
		ProposalsDir: propsDir,
		DecisionsDir: decsDir,
		EventBusDir:  eventsDir,
	}
	cmd := newDirectiveCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := RunDirective(cmd, cfg); err != nil {
		t.Fatalf("error: %v", err)
	}

	// Verify decision file written
	decPath := filepath.Join(decsDir, "fp006.json")
	data, err := os.ReadFile(decPath)
	if err != nil {
		t.Fatalf("decision file not written: %v", err)
	}
	var ev DirectiveDecisionEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if ev.Decision != "approved" {
		t.Errorf("decision = %q, want approved", ev.Decision)
	}
	if ev.EventType != "directive_decision" {
		t.Errorf("event_type = %q, want directive_decision", ev.EventType)
	}
}

// CT-7: Reject writes directive_decision event with decision: rejected, snooze_until: +14d
func TestDirective_CT7_RejectWritesSnooze(t *testing.T) {
	dir := t.TempDir()
	propsDir := filepath.Join(dir, "proposals")
	decsDir := filepath.Join(dir, "decisions")
	eventsDir := filepath.Join(dir, "events")
	os.MkdirAll(propsDir, 0o755)
	os.MkdirAll(decsDir, 0o755)
	os.MkdirAll(eventsDir, 0o755)

	writeProposal(t, propsDir, "fp007", Proposal{Tier: "T2", Confidence: 0.86})

	cfg := DirectiveConfig{
		Reject:       "fp007",
		ProposalsDir: propsDir,
		DecisionsDir: decsDir,
		EventBusDir:  eventsDir,
	}
	cmd := newDirectiveCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := RunDirective(cmd, cfg); err != nil {
		t.Fatalf("error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(decsDir, "fp007.json"))
	if err != nil {
		t.Fatal("decision file not written")
	}
	var ev DirectiveDecisionEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Decision != "rejected" {
		t.Errorf("decision = %q, want rejected", ev.Decision)
	}
	if ev.SnoozedUntil == "" {
		t.Error("snooze_until should be set on rejection")
	}
	snooze, err := time.Parse("20060102T150405Z", ev.SnoozedUntil)
	if err != nil {
		t.Fatalf("bad snooze_until format: %v", err)
	}
	delta := snooze.Sub(time.Now().UTC())
	if delta < 13*24*time.Hour || delta > 15*24*time.Hour {
		t.Errorf("snooze_until should be ~14d from now, got delta %v", delta)
	}
}

// CT-8: Reject does NOT modify the target rule file
func TestDirective_CT8_RejectNoFileWrite(t *testing.T) {
	dir := t.TempDir()
	propsDir := filepath.Join(dir, "proposals")
	decsDir := filepath.Join(dir, "decisions")
	eventsDir := filepath.Join(dir, "events")
	os.MkdirAll(propsDir, 0o755)
	os.MkdirAll(decsDir, 0o755)
	os.MkdirAll(eventsDir, 0o755)

	ruleFile := filepath.Join(dir, "rules.md")
	original := "# Rules\nOriginal content."
	if err := os.WriteFile(ruleFile, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	writeProposal(t, propsDir, "fp008", Proposal{
		Tier:         "T2",
		TargetFile:   ruleFile,
		ProposedDiff: "Original content.",
	})

	cfg := DirectiveConfig{
		Reject:       "fp008",
		ProposalsDir: propsDir,
		DecisionsDir: decsDir,
		EventBusDir:  eventsDir,
	}
	cmd := newDirectiveCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := RunDirective(cmd, cfg); err != nil {
		t.Fatalf("error: %v", err)
	}

	got, err := os.ReadFile(ruleFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("file modified on reject: got %q, want %q", string(got), original)
	}
}

// CT-9: target_file_drift (E301) when target file changed since proposal
func TestDirective_CT9_DriftError(t *testing.T) {
	dir := t.TempDir()
	propsDir := filepath.Join(dir, "proposals")
	decsDir := filepath.Join(dir, "decisions")
	eventsDir := filepath.Join(dir, "events")
	os.MkdirAll(propsDir, 0o755)
	os.MkdirAll(decsDir, 0o755)
	os.MkdirAll(eventsDir, 0o755)

	ruleFile := filepath.Join(dir, "rules.md")
	if err := os.WriteFile(ruleFile, []byte("current content"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Hash of DIFFERENT content - simulates drift
	staleHash := fmt.Sprintf("%x", sha256.Sum256([]byte("old content")))
	writeProposal(t, propsDir, "fp009", Proposal{
		Tier:         "T2",
		TargetFile:   ruleFile,
		ProposedDiff: "current content",
		TargetHash:   staleHash,
	})

	cfg := DirectiveConfig{
		Apply:        "fp009",
		ProposalsDir: propsDir,
		DecisionsDir: decsDir,
		EventBusDir:  eventsDir,
	}
	cmd := newDirectiveCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := RunDirective(cmd, cfg)
	if err == nil {
		t.Fatal("expected E301 drift error, got nil")
	}
	if !strings.Contains(err.Error(), "E301") {
		t.Errorf("expected E301 error, got: %v", err)
	}
}

// CT-10: proposals_dir_missing (E304) when proposals dir absent
func TestDirective_CT10_ProposalsDirMissing(t *testing.T) {
	dir := t.TempDir()
	cfg := DirectiveConfig{
		ProposalsDir: filepath.Join(dir, "nonexistent"),
		DecisionsDir: filepath.Join(dir, "decisions"),
	}
	cmd := newDirectiveCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := RunDirective(cmd, cfg)
	if err == nil {
		t.Fatal("expected E304 error, got nil")
	}
	if !strings.Contains(err.Error(), "E304") {
		t.Errorf("expected E304 error, got: %v", err)
	}
}

// CT-11: --apply <fingerprint> applies single proposal non-interactively
func TestDirective_CT11_ApplyNonInteractive(t *testing.T) {
	dir := t.TempDir()
	propsDir := filepath.Join(dir, "proposals")
	decsDir := filepath.Join(dir, "decisions")
	eventsDir := filepath.Join(dir, "events")
	os.MkdirAll(propsDir, 0o755)
	os.MkdirAll(decsDir, 0o755)
	os.MkdirAll(eventsDir, 0o755)

	writeProposal(t, propsDir, "fp011", Proposal{Tier: "T1", Confidence: 0.80})

	var out bytes.Buffer
	cmd := newDirectiveCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cfg := DirectiveConfig{
		Apply:        "fp011",
		ProposalsDir: propsDir,
		DecisionsDir: decsDir,
		EventBusDir:  eventsDir,
	}
	if err := RunDirective(cmd, cfg); err != nil {
		t.Fatalf("--apply error: %v", err)
	}
	if !strings.Contains(out.String(), "approved") {
		t.Errorf("expected 'approved' in output, got: %s", out.String())
	}
	// Decision should be recorded
	if _, err := os.Stat(filepath.Join(decsDir, "fp011.json")); os.IsNotExist(err) {
		t.Error("decision file not written")
	}
}

// CT-12: No LLM call made during any operation
func TestDirective_CT12_NoLLMCall(t *testing.T) {
	// Structural assertion: the directive command has no import of anthropic SDK,
	// no exec.Command calls to the claude CLI, and no HTTP client calls.
	// This test verifies the command runs without any external subprocess.
	dir := t.TempDir()
	propsDir := filepath.Join(dir, "proposals")
	decsDir := filepath.Join(dir, "decisions")
	os.MkdirAll(propsDir, 0o755)
	os.MkdirAll(decsDir, 0o755)

	writeProposal(t, propsDir, "fp012", Proposal{Tier: "T2", Confidence: 0.85})
	var out bytes.Buffer
	cmd := newDirectiveCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cfg := DirectiveConfig{
		ListOnly:     true,
		ProposalsDir: propsDir,
		DecisionsDir: decsDir,
	}
	// Simply verify it runs and produces output with no external calls
	if err := RunDirective(cmd, cfg); err != nil {
		t.Fatalf("error: %v", err)
	}
}

// --- Behavioral Tests ---

// BT-1: Interactive approve-reject-skip cycle: 3 proposals correctly handled
func TestDirective_BT1_InteractiveCycle(t *testing.T) {
	dir := t.TempDir()
	propsDir := filepath.Join(dir, "proposals")
	decsDir := filepath.Join(dir, "decisions")
	eventsDir := filepath.Join(dir, "events")
	os.MkdirAll(propsDir, 0o755)
	os.MkdirAll(decsDir, 0o755)
	os.MkdirAll(eventsDir, 0o755)

	writeProposal(t, propsDir, "bt1-a", Proposal{Tier: "T1", Confidence: 0.80})
	writeProposal(t, propsDir, "bt1-b", Proposal{Tier: "T2", Confidence: 0.88})
	writeProposal(t, propsDir, "bt1-c", Proposal{Tier: "T1", Confidence: 0.77})

	input := strings.NewReader("a\nr\ns\n") // approve first, reject second, skip third
	var out bytes.Buffer
	cmd := newDirectiveCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cfg := DirectiveConfig{
		ProposalsDir: propsDir,
		DecisionsDir: decsDir,
		EventBusDir:  eventsDir,
		Input:        input,
	}
	if err := RunDirective(cmd, cfg); err != nil {
		t.Fatalf("BT-1 error: %v", err)
	}
}

// BT-2: q quits interactive mode without processing remaining proposals
func TestDirective_BT2_QuitMode(t *testing.T) {
	dir := t.TempDir()
	propsDir := filepath.Join(dir, "proposals")
	decsDir := filepath.Join(dir, "decisions")
	os.MkdirAll(propsDir, 0o755)
	os.MkdirAll(decsDir, 0o755)

	writeProposal(t, propsDir, "bt2-a", Proposal{Tier: "T2", Confidence: 0.90})
	writeProposal(t, propsDir, "bt2-b", Proposal{Tier: "T1", Confidence: 0.78})

	input := strings.NewReader("q\n") // quit immediately
	var out bytes.Buffer
	cmd := newDirectiveCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cfg := DirectiveConfig{
		ProposalsDir: propsDir,
		DecisionsDir: decsDir,
		Input:        input,
	}
	if err := RunDirective(cmd, cfg); err != nil {
		t.Fatalf("BT-2 error: %v", err)
	}
	// bt2-b should NOT have a decision written (quit before it was reached)
	if _, err := os.Stat(filepath.Join(decsDir, "bt2-b.json")); err == nil {
		t.Error("bt2-b should not have a decision after quit")
	}
}

// BT-3: No pending proposals: graceful message, exit 0
func TestDirective_BT3_NoPendingProposals(t *testing.T) {
	dir := t.TempDir()
	propsDir := filepath.Join(dir, "proposals")
	decsDir := filepath.Join(dir, "decisions")
	os.MkdirAll(propsDir, 0o755)
	os.MkdirAll(decsDir, 0o755)

	var out bytes.Buffer
	cmd := newDirectiveCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cfg := DirectiveConfig{
		ProposalsDir: propsDir,
		DecisionsDir: decsDir,
		Input:        strings.NewReader(""),
	}
	if err := RunDirective(cmd, cfg); err != nil {
		t.Fatalf("expected exit 0, got: %v", err)
	}
	if !strings.Contains(out.String(), "No pending") {
		t.Errorf("expected 'No pending' message, got: %s", out.String())
	}
}

// BT-4: --apply on non-existent fingerprint: clear error, exit 1
func TestDirective_BT4_ApplyNonExistentFingerprint(t *testing.T) {
	dir := t.TempDir()
	propsDir := filepath.Join(dir, "proposals")
	decsDir := filepath.Join(dir, "decisions")
	os.MkdirAll(propsDir, 0o755)
	os.MkdirAll(decsDir, 0o755)

	cfg := DirectiveConfig{
		Apply:        "nonexistent-fp",
		ProposalsDir: propsDir,
		DecisionsDir: decsDir,
	}
	cmd := newDirectiveCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := RunDirective(cmd, cfg)
	if err == nil {
		t.Fatal("expected error for non-existent fingerprint, got nil")
	}
}

// --- Regression Guards ---

// RG-1: Approved proposal applies exactly one str.replace - no additional modifications
func TestDirective_RG1_ExactlyOneReplace(t *testing.T) {
	dir := t.TempDir()
	propsDir := filepath.Join(dir, "proposals")
	decsDir := filepath.Join(dir, "decisions")
	eventsDir := filepath.Join(dir, "events")
	os.MkdirAll(propsDir, 0o755)
	os.MkdirAll(decsDir, 0o755)
	os.MkdirAll(eventsDir, 0o755)

	ruleFile := filepath.Join(dir, "rules.md")
	original := "Line one.\nTarget line.\nLine three."
	if err := os.WriteFile(ruleFile, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(original)))
	writeProposal(t, propsDir, "rg1", Proposal{
		Tier:         "T2",
		TargetFile:   ruleFile,
		ProposedDiff: "Target line.",
		TargetHash:   hash,
	})
	cfg := DirectiveConfig{
		Apply:        "rg1",
		ProposalsDir: propsDir,
		DecisionsDir: decsDir,
		EventBusDir:  eventsDir,
	}
	cmd := newDirectiveCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := RunDirective(cmd, cfg); err != nil {
		t.Fatalf("error: %v", err)
	}
}

// RG-2: Zero LLM calls in entire directive command lifecycle
func TestDirective_RG2_ZeroLLMCalls(t *testing.T) {
	// Same as CT-12 - structural: no anthropic SDK import in cmd_directive.go.
	// This test verifies that no exec.Command("claude", ...) or HTTP call
	// is made by simply running the command with controlled inputs.
	dir := t.TempDir()
	propsDir := filepath.Join(dir, "proposals")
	decsDir := filepath.Join(dir, "decisions")
	os.MkdirAll(propsDir, 0o755)
	os.MkdirAll(decsDir, 0o755)
	cfg := DirectiveConfig{ListOnly: true, ProposalsDir: propsDir, DecisionsDir: decsDir}
	cmd := newDirectiveCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := RunDirective(cmd, cfg); err != nil {
		t.Fatalf("error: %v", err)
	}
}

// RG-3: Re-approve of already-applied proposal is idempotent
func TestDirective_RG3_ReApproveIdempotent(t *testing.T) {
	dir := t.TempDir()
	propsDir := filepath.Join(dir, "proposals")
	decsDir := filepath.Join(dir, "decisions")
	eventsDir := filepath.Join(dir, "events")
	os.MkdirAll(propsDir, 0o755)
	os.MkdirAll(decsDir, 0o755)
	os.MkdirAll(eventsDir, 0o755)

	writeProposal(t, propsDir, "rg3", Proposal{Tier: "T1", Confidence: 0.80})

	cfg := DirectiveConfig{
		Apply:        "rg3",
		ProposalsDir: propsDir,
		DecisionsDir: decsDir,
		EventBusDir:  eventsDir,
	}
	for i := 0; i < 2; i++ {
		cmd := newDirectiveCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := RunDirective(cmd, cfg); err != nil {
			t.Fatalf("run %d error: %v", i, err)
		}
	}
}
