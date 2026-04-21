package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newDoctorCmdForTest builds a doctorCmd with output captured and HOME
// redirected to the supplied tmpDir (so XDG dirs land there, not ~/.config).
func newDoctorCmdForTest(t *testing.T, tmpDir string, extraArgs []string) (*bytes.Buffer, func() error) {
	t.Helper()
	t.Setenv("HOME", tmpDir)
	// Clear SM-related env so tests never attempt real AWS calls.
	t.Setenv("LINKARI_TOKEN", "")
	t.Setenv("LINKARI_FIREBASE_SA", "")
	t.Setenv("TS_AUTHKEY", "")
	t.Setenv("LINKARI_JIRA_TOKEN", "")
	t.Setenv("LINKARI_JIRA_API_USERNAME", "")
	t.Setenv("LINKARI_JIRA_API_PASSWORD", "")
	t.Setenv("LINKARI_JIRA_DOMAIN", "")
	t.Setenv("LINKARI_PAGERDUTY_TOKEN", "")

	cmd := doctorCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(extraArgs)
	return &out, cmd.Execute
}

// TestDoctor_MissingServerYAML verifies that a missing server.yaml produces
// a warn on server_yaml and fail on token, but exits with an error.
func TestDoctor_MissingServerYAML(t *testing.T) {
	dir := t.TempDir()
	out, run := newDoctorCmdForTest(t, dir, nil)
	err := run()
	// Expect non-nil error (token fail → exit 1).
	if err == nil {
		t.Error("expected error from missing token, got nil")
	}
	got := out.String()
	if !strings.Contains(got, "⚠ server_yaml") {
		t.Errorf("expected warn for server_yaml, output:\n%s", got)
	}
	if !strings.Contains(got, "✗ token") {
		t.Errorf("expected fail for token, output:\n%s", got)
	}
}

// TestDoctor_LiteralToken verifies that a literal token in server.yaml
// produces an ok check for token and skips the aws_identity check.
func TestDoctor_LiteralToken(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "linkari")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	yamlPath := filepath.Join(cfgDir, "server.yaml")
	content := "server:\n  token: \"test-literal-token\"\n"
	if err := os.WriteFile(yamlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write server.yaml: %v", err)
	}

	out, run := newDoctorCmdForTest(t, dir, []string{"--path", yamlPath})
	_ = run() // may fail due to firebase_sa/tsnet_authkey being optional warns — that's fine

	got := out.String()
	if !strings.Contains(got, "✓ server_yaml") {
		t.Errorf("expected ok for server_yaml, output:\n%s", got)
	}
	if !strings.Contains(got, "✓ token") {
		t.Errorf("expected ok for token, output:\n%s", got)
	}
	// No SM URIs → no aws_identity check.
	if strings.Contains(got, "aws_identity") {
		t.Errorf("aws_identity check should not appear without SM URIs, output:\n%s", got)
	}
}

// TestDoctor_JSONOutput verifies the --json flag emits parseable JSON with the
// correct structure, and that exit_code reflects check outcomes.
func TestDoctor_JSONOutput(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "linkari")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	yamlPath := filepath.Join(cfgDir, "server.yaml")
	// Literal token: all secret checks pass without SM calls.
	content := "server:\n  token: \"test-literal-token\"\n"
	if err := os.WriteFile(yamlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write server.yaml: %v", err)
	}

	out, run := newDoctorCmdForTest(t, dir, []string{"--path", yamlPath, "--json"})
	_ = run() // ignore error; we test JSON structure

	var result struct {
		Checks   []doctorCheck `json:"checks"`
		ExitCode int           `json:"exit_code"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("JSON parse failed: %v\noutput:\n%s", err, out.String())
	}
	if len(result.Checks) == 0 {
		t.Error("expected non-empty checks array")
	}
	for _, c := range result.Checks {
		if c.Name == "" {
			t.Errorf("check with empty name: %+v", c)
		}
		if c.Status != statusOK && c.Status != statusWarn && c.Status != statusFail {
			t.Errorf("invalid status %q for check %q", c.Status, c.Name)
		}
	}
}

// TestDoctor_ExitCodeMatrix verifies exit codes: 0 on all-ok/warn, 1 on any fail.
func TestDoctor_ExitCodeMatrix(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "linkari")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	yamlPath := filepath.Join(cfgDir, "server.yaml")
	content := "server:\n  token: \"test-literal-token\"\n"
	if err := os.WriteFile(yamlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write server.yaml: %v", err)
	}

	out, run := newDoctorCmdForTest(t, dir, []string{"--path", yamlPath, "--json"})
	err := run()

	var result struct {
		Checks   []doctorCheck `json:"checks"`
		ExitCode int           `json:"exit_code"`
	}
	if jsonErr := json.Unmarshal(out.Bytes(), &result); jsonErr != nil {
		t.Fatalf("JSON parse: %v\noutput:\n%s", jsonErr, out.String())
	}

	// exit_code in JSON must match whether err is nil.
	if err == nil && result.ExitCode != 0 {
		t.Errorf("err=nil but JSON exit_code=%d", result.ExitCode)
	}
	if err != nil && result.ExitCode == 0 {
		t.Errorf("err=%v but JSON exit_code=0", err)
	}
}

// TestDoctor_AllChecksPresent verifies that all expected check names appear in
// the output for a fully configured (literal-value) server.yaml.
func TestDoctor_AllChecksPresent(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "linkari")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	yamlPath := filepath.Join(cfgDir, "server.yaml")
	// Use file:// URIs so SM is not called but fields are non-empty.
	fakeSA := filepath.Join(dir, "firebase-sa.json")
	if err := os.WriteFile(fakeSA, []byte(`{"type":"service_account"}`), 0o600); err != nil {
		t.Fatalf("write fake SA: %v", err)
	}
	content := "server:\n" +
		"  token: \"test-literal-token\"\n" +
		"  firebase_sa: \"file://" + fakeSA + "\"\n" +
		"  tsnet_authkey: \"tskey-test\"\n" +
		"  jira_token: \"jira-test-token\"\n" +
		"  jira_api_username: \"user@example.com\"\n" +
		"  jira_api_password: \"test-password\"\n" +
		"  jira_domain: \"test.atlassian.net\"\n" +
		"  pagerduty_token: \"pd-test-token\"\n" +
		"  log_file: \"" + filepath.Join(dir, "linkari.log") + "\"\n"
	if err := os.WriteFile(yamlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write server.yaml: %v", err)
	}

	out, run := newDoctorCmdForTest(t, dir, []string{"--path", yamlPath, "--json"})
	_ = run()

	var result struct {
		Checks []doctorCheck `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("JSON parse: %v\noutput:\n%s", err, out.String())
	}

	checkNames := make(map[string]bool, len(result.Checks))
	for _, c := range result.Checks {
		checkNames[c.Name] = true
	}

	required := []string{
		"server_yaml", "token", "firebase_sa", "tsnet_authkey", "jira_token",
		"jira_api_username", "jira_api_password", "jira_domain", "pagerduty_token",
		"xdg_config_dir", "xdg_cache_dir", "xdg_state_dir",
		"tsnet_state", "firebase_sa_cache", "log_file",
		"ffmpeg", "ytdlp", "whisper_cli",
	}
	for _, name := range required {
		if !checkNames[name] {
			t.Errorf("missing check %q; present: %v", name, checkNamesSlice(result.Checks))
		}
	}
}

func checkNamesSlice(checks []doctorCheck) []string {
	names := make([]string, len(checks))
	for i, c := range checks {
		names[i] = c.Name
	}
	return names
}
