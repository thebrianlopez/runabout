package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProfileLintCmdValid(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "eng.yaml")
	if err := os.WriteFile(yamlPath, []byte(validEngYAML), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := profileCmd()
	cmd.SetArgs([]string{"lint", "--fixtures", t.TempDir(), yamlPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected lint pass, got %v", err)
	}
}

func TestProfileLintCmdBadWeights(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "broken.yaml")
	bad := validEngYAML // mutate one weight
	bad = bad[:len(bad)-0] // no-op so we can edit below
	// Replace the first weight 20 → 21 to break the sum.
	bad = replaceFirst(bad, "weight: 20", "weight: 21")
	if err := os.WriteFile(yamlPath, []byte(bad), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := profileCmd()
	cmd.SetArgs([]string{"lint", "--fixtures", t.TempDir(), yamlPath})
	if err := cmd.Execute(); err == nil {
		t.Fatalf("expected lint failure, got nil")
	}
}

func writeFixture(t *testing.T, dir, id, profile string) {
	t.Helper()
	body := `{"id":"` + id + `","profile":"` + profile + `"}`
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestProfileLintMinFixturesAllPass(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "eng.yaml")
	if err := os.WriteFile(yamlPath, []byte(validEngYAML), 0644); err != nil {
		t.Fatal(err)
	}
	fxDir := t.TempDir()
	writeFixture(t, fxDir, "a", "eng")
	writeFixture(t, fxDir, "b", "eng")
	cmd := profileCmd()
	cmd.SetArgs([]string{"lint", "--fixtures", fxDir, "--min-fixtures", "2", yamlPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected pass, got %v", err)
	}
}

func TestProfileLintMinFixturesHardFail(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "eng.yaml")
	if err := os.WriteFile(yamlPath, []byte(validEngYAML), 0644); err != nil {
		t.Fatal(err)
	}
	fxDir := t.TempDir()
	writeFixture(t, fxDir, "a", "eng")
	cmd := profileCmd()
	cmd.SetArgs([]string{"lint", "--fixtures", fxDir, "--min-fixtures", "3", yamlPath})
	if err := cmd.Execute(); err == nil {
		t.Fatalf("expected hard-fail, got nil")
	}
}

func TestProfileLintWarnOnly(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "eng.yaml")
	if err := os.WriteFile(yamlPath, []byte(validEngYAML), 0644); err != nil {
		t.Fatal(err)
	}
	fxDir := t.TempDir()
	writeFixture(t, fxDir, "a", "eng")
	writeFixture(t, fxDir, "b", "eng")
	// 2 fixtures: below warn threshold (5), but min-fixtures=1 should pass.
	cmd := profileCmd()
	cmd.SetArgs([]string{"lint", "--fixtures", fxDir, "--min-fixtures", "1", yamlPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected warn-only pass, got %v", err)
	}
}

func TestFilterFixturesByProfile(t *testing.T) {
	fx := []Fixture{
		{ID: "a", Profile: "eng"},
		{ID: "b", Profile: "dining"},
		{ID: "c", Profile: "eng"},
	}
	got := filterFixturesByProfile(fx, "eng")
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Fatalf("unexpected: %+v", got)
	}
	if len(filterFixturesByProfile(fx, "")) != 3 {
		t.Fatalf("empty profile should pass through")
	}
	if len(filterFixturesByProfile(fx, "music")) != 0 {
		t.Fatalf("missing profile should return empty")
	}
}

func replaceFirst(s, old, new string) string {
	i := indexOf(s, old)
	if i < 0 {
		return s
	}
	return s[:i] + new + s[i+len(old):]
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

const validEngYAML = `id: eng
version: 1
schema_version: triage_verdict_v1
persona_intro: "You are a triage assistant."
noise_gate:
  min_chars: 200
  skip_label: "no extractable content"
persona_body: |
  ## My Context

  Test body.
verdict_prompt: "what is this?"
rubric:
  - name: A
    weight: 20
    rationale: "a"
  - name: B
    weight: 20
    rationale: "b"
  - name: C
    weight: 20
    rationale: "c"
  - name: D
    weight: 20
    rationale: "d"
  - name: E
    weight: 20
    rationale: "e"
action_items:
  count: "2-3"
  horizon_days: 7
key_facts:
  count: "3-5"
`
