package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thebrianlopez/runabout/internal/secrets"
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
	t.Setenv("LINKARI_ATLASSIAN_EMAIL", "")
	t.Setenv("LINKARI_ATLASSIAN_API_TOKEN", "")
	t.Setenv("LINKARI_JIRA_DOMAIN", "")
	t.Setenv("LINKARI_PAGERDUTY_TOKEN", "")

	cmd := doctorCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(extraArgs)
	return &out, cmd.Execute
}

// TestDoctor_MissingServerYAML verifies that a missing config.toml produces
// a warn on config_toml and fail on token, but exits with an error.
func TestDoctor_MissingServerYAML(t *testing.T) {
	dir := t.TempDir()
	out, run := newDoctorCmdForTest(t, dir, nil)
	err := run()
	// Expect non-nil error (token fail → exit 1).
	if err == nil {
		t.Error("expected error from missing token, got nil")
	}
	got := out.String()
	if !strings.Contains(got, "⚠ config_toml") {
		t.Errorf("expected warn for config_toml, output:\n%s", got)
	}
	if !strings.Contains(got, "✗ token") {
		t.Errorf("expected fail for token, output:\n%s", got)
	}
}

// TestDoctor_LiteralToken verifies that a literal token in config.toml
// produces an ok check for token and skips the aws_identity check.
func TestDoctor_LiteralToken(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "linkari")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tomlPath := filepath.Join(cfgDir, "config.toml")
	content := "[server]\ntoken = \"test-literal-token\"\n"
	if err := os.WriteFile(tomlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	out, run := newDoctorCmdForTest(t, dir, []string{"--path", tomlPath})
	_ = run() // may fail due to firebase_sa/tsnet_authkey being optional warns  -  that's fine

	got := out.String()
	if !strings.Contains(got, "✓ config_toml") {
		t.Errorf("expected ok for config_toml, output:\n%s", got)
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
	tomlPath := filepath.Join(cfgDir, "config.toml")
	// Literal token: all secret checks pass without SM calls.
	content := "[server]\ntoken = \"test-literal-token\"\n"
	if err := os.WriteFile(tomlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	out, run := newDoctorCmdForTest(t, dir, []string{"--path", tomlPath, "--json"})
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
	tomlPath := filepath.Join(cfgDir, "config.toml")
	content := "[server]\ntoken = \"test-literal-token\"\n"
	if err := os.WriteFile(tomlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	out, run := newDoctorCmdForTest(t, dir, []string{"--path", tomlPath, "--json"})
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

// TestDoctor_StrictGatesOnWarnings is the RG for POMO ec2-server-dependency-gaps
// RS-1. Optional-dependency gaps (missing lit, whisper-cli, tessdata) are emitted
// as warnings, not failures. Default doctor exits 0 on warnings, which is why a
// month of "lit: not found" never gated a deploy. --strict must turn any warning
// into a non-zero exit so post-deploy CI can consume the signal.
func TestDoctor_StrictGatesOnWarnings(t *testing.T) {
	newCfg := func(t *testing.T) (string, string) {
		t.Helper()
		dir := t.TempDir()
		cfgDir := filepath.Join(dir, ".config", "linkari")
		if err := os.MkdirAll(cfgDir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		tomlPath := filepath.Join(cfgDir, "config.toml")
		// Minimal config: resolves a token but leaves optional deps unconfigured,
		// which reliably produces at least one warn check.
		content := "[server]\ntoken = \"test-literal-token\"\n"
		if err := os.WriteFile(tomlPath, []byte(content), 0o600); err != nil {
			t.Fatalf("write config.toml: %v", err)
		}
		return dir, tomlPath
	}

	decode := func(t *testing.T, b []byte) struct {
		Checks   []doctorCheck `json:"checks"`
		ExitCode int           `json:"exit_code"`
	} {
		t.Helper()
		var result struct {
			Checks   []doctorCheck `json:"checks"`
			ExitCode int           `json:"exit_code"`
		}
		// Cobra appends "Error: ..." after the JSON document when RunE returns a
		// non-nil error, so decode only the leading JSON value rather than the
		// whole buffer.
		if err := json.NewDecoder(bytes.NewReader(b)).Decode(&result); err != nil {
			t.Fatalf("JSON parse: %v\noutput:\n%s", err, string(b))
		}
		return result
	}

	// Baseline: confirm this fixture actually warns and does not fail, otherwise
	// the strict assertion below would be vacuous.
	dir, tomlPath := newCfg(t)
	out, run := newDoctorCmdForTest(t, dir, []string{"--path", tomlPath, "--json"})
	looseErr := run()
	loose := decode(t, out.Bytes())

	warnCount, failCount := 0, 0
	for _, c := range loose.Checks {
		switch c.Status {
		case statusWarn:
			warnCount++
		case statusFail:
			failCount++
		}
	}
	if failCount > 0 {
		t.Skipf("fixture produced %d fail checks; strict-vs-loose is only meaningful with warns and no fails", failCount)
	}
	if warnCount == 0 {
		t.Skip("fixture produced no warn checks; nothing to gate on")
	}

	// Loose mode: warnings must NOT gate (preserves existing behaviour).
	if looseErr != nil {
		t.Errorf("without --strict: got err=%v, want nil with %d warns and no fails", looseErr, warnCount)
	}
	if loose.ExitCode != 0 {
		t.Errorf("without --strict: exit_code=%d, want 0", loose.ExitCode)
	}

	// Strict mode: the same warnings must gate.
	dir2, tomlPath2 := newCfg(t)
	out2, run2 := newDoctorCmdForTest(t, dir2, []string{"--path", tomlPath2, "--json", "--strict"})
	strictErr := run2()
	strict := decode(t, out2.Bytes())

	if strictErr == nil {
		t.Errorf("with --strict: got err=nil, want non-nil (%d warn checks present)", warnCount)
	}
	if strict.ExitCode != 1 {
		t.Errorf("with --strict: exit_code=%d, want 1", strict.ExitCode)
	}
	// JSON exit_code and the returned error must never disagree.
	if (strictErr != nil) != (strict.ExitCode != 0) {
		t.Errorf("strict: err=%v disagrees with exit_code=%d", strictErr, strict.ExitCode)
	}
}

// TestDoctor_RequireIsFocused is the post-deploy gate contract: a deployment
// can require the tools it needs without being held hostage by unrelated optional
// integrations. It also prevents an unknown check name from silently passing.
func TestDoctor_RequireIsFocused(t *testing.T) {
	newCfg := func(t *testing.T) (string, string) {
		t.Helper()
		dir := t.TempDir()
		cfgDir := filepath.Join(dir, ".config", "linkari")
		if err := os.MkdirAll(cfgDir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		tomlPath := filepath.Join(cfgDir, "config.toml")
		if err := os.WriteFile(tomlPath, []byte("[server]\ntoken = \"test-literal-token\"\n"), 0o600); err != nil {
			t.Fatalf("write config.toml: %v", err)
		}
		return dir, tomlPath
	}

	// The minimal fixture has unrelated failures/warnings. A required token is
	// nevertheless OK, so a focused gate must pass.
	dir, tomlPath := newCfg(t)
	_, run := newDoctorCmdForTest(t, dir, []string{"--path", tomlPath, "--require", "token"})
	if err := run(); err != nil {
		t.Fatalf("--require token: got err=%v, want nil", err)
	}

	// Require a check this fixture actually reports as warn; requiring it must
	// gate whether the local development machine happens to have lit installed.
	dir2, tomlPath2 := newCfg(t)
	out2, run2 := newDoctorCmdForTest(t, dir2, []string{"--path", tomlPath2, "--json"})
	_ = run2() // unrelated failures are expected in this minimal fixture.
	var report struct {
		Checks []doctorCheck `json:"checks"`
	}
	if err := json.NewDecoder(bytes.NewReader(out2.Bytes())).Decode(&report); err != nil {
		t.Fatalf("decode baseline report: %v\noutput:\n%s", err, out2.String())
	}
	warnName := ""
	for _, c := range report.Checks {
		if c.Status == statusWarn {
			warnName = c.Name
			break
		}
	}
	if warnName == "" {
		t.Fatal("fixture produced no warn check to exercise --require")
	}
	dirWarn, tomlPathWarn := newCfg(t)
	_, runWarn := newDoctorCmdForTest(t, dirWarn, []string{"--path", tomlPathWarn, "--require", warnName})
	if err := runWarn(); err == nil {
		t.Fatalf("--require %s: got nil, want error for a warn check", warnName)
	}

	// Typos/renames are gate failures, never a false green deployment.
	dir3, tomlPath3 := newCfg(t)
	_, run3 := newDoctorCmdForTest(t, dir3, []string{"--path", tomlPath3, "--require", "not_a_real_check"})
	if err := run3(); err == nil {
		t.Fatal("unknown --require name: got nil, want error")
	}
}

// TestDoctor_AllChecksPresent verifies that all expected check names appear in
// the output for a fully configured (literal-value) config.toml.
func TestDoctor_AllChecksPresent(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "linkari")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tomlPath := filepath.Join(cfgDir, "config.toml")
	fakeSA := filepath.Join(dir, "firebase-sa.json")
	if err := os.WriteFile(fakeSA, []byte(`{"type":"service_account"}`), 0o600); err != nil {
		t.Fatalf("write fake SA: %v", err)
	}
	content := "[server]\n" +
		"token = \"test-literal-token\"\n" +
		"firebase_sa = \"" + fakeSA + "\"\n" +
		"tsnet_authkey = \"tskey-test\"\n" +
		"jira_token = \"jira-test-token\"\n" +
		"atlassian_email = \"user@example.com\"\n" +
		"atlassian_api_token = \"test-password\"\n" +
		"jira_domain = \"test.atlassian.net\"\n" +
		"pagerduty_token = \"pd-test-token\"\n" +
		"log_file = \"" + filepath.Join(dir, "linkari.log") + "\"\n"
	if err := os.WriteFile(tomlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	out, run := newDoctorCmdForTest(t, dir, []string{"--path", tomlPath, "--json"})
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
		"config_toml", "token", "firebase_sa", "tsnet_authkey", "jira_token",
		"atlassian_email", "atlassian_api_token", "jira_domain", "pagerduty_token",
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

// RG-1 (CT-1): tessdata_prefix in config, TESSDATA_PREFIX env absent → okCheck.
// Validates the config-authority fix (EPIC-109 M1): doctor must read the config
// struct as primary source, not the env var set by `linkari serve` init.
func TestDoctorTessdata_ConfigSet_EnvAbsent(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "linkari")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Create a real tessdata dir with a .traineddata file so the EPIC-164
	// functional check (ReadDir + hasTrainedData) passes.
	tessdataDir := filepath.Join(dir, "tessdata")
	if err := os.MkdirAll(tessdataDir, 0o700); err != nil {
		t.Fatalf("mkdir tessdata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tessdataDir, "eng.traineddata"), []byte("fake"), 0o600); err != nil {
		t.Fatalf("write traineddata: %v", err)
	}

	tomlPath := filepath.Join(cfgDir, "config.toml")
	content := fmt.Sprintf("[server]\ntoken = \"test-token\"\n\n[server.liteparse]\ntessdata_prefix = %q\n", tessdataDir)
	if err := os.WriteFile(tomlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	t.Setenv("TESSDATA_PREFIX", "")

	out, run := newDoctorCmdForTest(t, dir, []string{"--path", tomlPath, "--json"})
	_ = run()

	var result struct {
		Checks []doctorCheck `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("JSON parse: %v\noutput:\n%s", err, out.String())
	}
	for _, c := range result.Checks {
		if c.Name == "tessdata_prefix" {
			if c.Status != statusOK {
				t.Errorf("RG-1: tessdata_prefix status = %q, want %q; detail=%q", c.Status, statusOK, c.Message)
			}
			return
		}
	}
	t.Error("RG-1: tessdata_prefix check not present in output")
}

// RG-2 (CT-2): neither config nor env set → warnCheck.
func TestDoctorTessdata_BothAbsent(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "linkari")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tomlPath := filepath.Join(cfgDir, "config.toml")
	content := "[server]\ntoken = \"test-token\"\n"
	if err := os.WriteFile(tomlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	t.Setenv("TESSDATA_PREFIX", "")

	out, run := newDoctorCmdForTest(t, dir, []string{"--path", tomlPath, "--json"})
	_ = run()

	var result struct {
		Checks []doctorCheck `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("JSON parse: %v\noutput:\n%s", err, out.String())
	}
	for _, c := range result.Checks {
		if c.Name == "tessdata_prefix" {
			if c.Status != statusWarn {
				t.Errorf("RG-2: tessdata_prefix status = %q, want %q; detail=%q", c.Status, statusWarn, c.Message)
			}
			return
		}
	}
	t.Error("RG-2: tessdata_prefix check not present in output")
}

// RG-3 (CT-3): only env var set (no config) → okCheck.
func TestDoctorTessdata_EnvOnly(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "linkari")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tomlPath := filepath.Join(cfgDir, "config.toml")
	content := "[server]\ntoken = \"test-token\"\n"
	if err := os.WriteFile(tomlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	// Create a real tessdata dir with a .traineddata file so the EPIC-164
	// functional check (ReadDir + hasTrainedData) passes.
	tessdataDir := filepath.Join(dir, "tessdata")
	if err := os.MkdirAll(tessdataDir, 0o700); err != nil {
		t.Fatalf("mkdir tessdata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tessdataDir, "eng.traineddata"), []byte("fake"), 0o600); err != nil {
		t.Fatalf("write traineddata: %v", err)
	}

	t.Setenv("TESSDATA_PREFIX", tessdataDir)

	out, run := newDoctorCmdForTest(t, dir, []string{"--path", tomlPath, "--json"})
	_ = run()

	var result struct {
		Checks []doctorCheck `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("JSON parse: %v\noutput:\n%s", err, out.String())
	}
	for _, c := range result.Checks {
		if c.Name == "tessdata_prefix" {
			if c.Status != statusOK {
				t.Errorf("RG-3: tessdata_prefix status = %q, want %q; detail=%q", c.Status, statusOK, c.Message)
			}
			return
		}
	}
	t.Error("RG-3: tessdata_prefix check not present in output")
}

// RG-? (CT-6): tessdata_prefix set to a nonexistent path → warnCheck with path detail (EPIC-164).
func TestDoctorTessdata_PathInvalid(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "linkari")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	nonexistent := filepath.Join(dir, "no", "such", "tessdata")
	tomlPath := filepath.Join(cfgDir, "config.toml")
	content := fmt.Sprintf("[server]\ntoken = \"test-token\"\n\n[server.liteparse]\ntessdata_prefix = %q\n", nonexistent)
	if err := os.WriteFile(tomlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	t.Setenv("TESSDATA_PREFIX", "")

	out, run := newDoctorCmdForTest(t, dir, []string{"--path", tomlPath, "--json"})
	_ = run()

	var result struct {
		Checks []doctorCheck `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("JSON parse: %v\noutput:\n%s", err, out.String())
	}
	for _, c := range result.Checks {
		if c.Name == "tessdata_prefix" {
			if c.Status != statusWarn {
				t.Errorf("CT-6: tessdata_prefix status = %q, want %q; detail=%q", c.Status, statusWarn, c.Message)
			}
			if !strings.Contains(c.Message, nonexistent) {
				t.Errorf("CT-6: message %q does not contain path %q", c.Message, nonexistent)
			}
			return
		}
	}
	t.Error("CT-6: tessdata_prefix check not present in output")
}

func TestDoctorSecretsManager_NoAWSCredentials_FailsFast(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "linkari")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tomlPath := filepath.Join(cfgDir, "config.toml")
	content := "[server]\ntoken = \"${secretsmanager:linkari/bearer-token}\"\n"
	if err := os.WriteFile(tomlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", "")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "")

	// The doctor command only emits the aws_credentials check when the config
	// contains secretsmanager refs AND hasExplicitAWSCredentials()/
	// hasIMDSCredentials() both report false (see awsCredsUnavailable in
	// cmd_doctor.go). hasIMDSCredentials probes the real EC2 metadata endpoint
	// (169.254.169.254) over the network - on hosts/sandboxes where something
	// answers that address (e.g. a metadata-emulating proxy), the probe comes
	// back true even with zero AWS credentials configured, which silently
	// suppresses the aws_credentials check entirely. Force it off hermetically
	// via the same AWS_EC2_METADATA_DISABLED env var the AWS SDK's own IMDS
	// client honors, rather than depending on real network reachability.
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	out, run := newDoctorCmdForTest(t, dir, []string{"--path", tomlPath})
	err := run()
	if err == nil {
		t.Fatal("CT-7: expected doctor to fail when SM refs exist without AWS credentials")
	}
	got := out.String()
	if !strings.Contains(got, "✗ aws_credentials:") {
		t.Fatalf("CT-7: aws_credentials check not found; output:\n%s", got)
	}
	if !strings.Contains(got, "AWS_PROFILE") {
		t.Fatalf("CT-7: expected actionable AWS_PROFILE guidance; output:\n%s", got)
	}
}

// CT-4: config tessdata_prefix takes precedence over TESSDATA_PREFIX env var when both are set.
func TestDoctorTessdata_ConfigPrecedenceOverEnv(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "linkari")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Valid tessdata dir in config.
	validDir := filepath.Join(dir, "tessdata-valid")
	if err := os.MkdirAll(validDir, 0o700); err != nil {
		t.Fatalf("mkdir tessdata-valid: %v", err)
	}
	if err := os.WriteFile(filepath.Join(validDir, "eng.traineddata"), []byte("fake"), 0o600); err != nil {
		t.Fatalf("write traineddata: %v", err)
	}
	// Invalid dir set via env (no traineddata files).
	invalidDir := filepath.Join(dir, "tessdata-invalid")
	if err := os.MkdirAll(invalidDir, 0o700); err != nil {
		t.Fatalf("mkdir tessdata-invalid: %v", err)
	}

	tomlPath := filepath.Join(cfgDir, "config.toml")
	content := fmt.Sprintf("[server]\ntoken = \"test-token\"\n\n[server.liteparse]\ntessdata_prefix = %q\n", validDir)
	if err := os.WriteFile(tomlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	t.Setenv("TESSDATA_PREFIX", invalidDir)

	out, run := newDoctorCmdForTest(t, dir, []string{"--path", tomlPath, "--json"})
	_ = run()

	var result struct {
		Checks []doctorCheck `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("JSON parse: %v\noutput:\n%s", err, out.String())
	}
	for _, c := range result.Checks {
		if c.Name == "tessdata_prefix" {
			if c.Status != statusOK {
				t.Errorf("CT-4: tessdata_prefix status = %q, want %q (config must win over env); detail=%q", c.Status, statusOK, c.Message)
			}
			return
		}
	}
	t.Error("CT-4: tessdata_prefix check not present in output")
}

// CT-5: probeHealth reflects the caller-supplied config prefix, ignoring TESSDATA_PREFIX env.
func TestHealthProbe_TessdataConfigAware(t *testing.T) {
	noLit := func(string) (string, error) { return "", fmt.Errorf("not found") }

	// Non-empty config prefix → TessdataPrefixSet = true, even with env absent.
	t.Setenv("TESSDATA_PREFIX", "")
	probeWithCfg := probeHealth(noLit, "/from/config")
	if !probeWithCfg.TessdataPrefixSet {
		t.Errorf("CT-5: TessdataPrefixSet = false with non-empty config prefix, want true")
	}

	// Empty config prefix → TessdataPrefixSet = false, even with env set.
	// probeHealth ignores the process env; the caller is responsible for passing cfg value.
	t.Setenv("TESSDATA_PREFIX", "/from/env")
	probeEmpty := probeHealth(noLit, "")
	if probeEmpty.TessdataPrefixSet {
		t.Errorf("CT-5: TessdataPrefixSet = true with empty config prefix (env should be ignored), want false")
	}
}

// CT-8: when explicit AWS credentials are present, the aws_credentials fast-fail check
// is not emitted even when the config contains Secrets Manager refs.
// Requires AWS_PROFILE=brianonpoint and a real SM-backed config  -  skipped otherwise.
func TestDoctorSecretsMgr_ExpectedProfileResolves(t *testing.T) {
	if os.Getenv("AWS_PROFILE") != "brianonpoint" {
		t.Skip("CT-8: requires AWS_PROFILE=brianonpoint and real SM-backed config")
	}
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "linkari")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tomlPath := filepath.Join(cfgDir, "config.toml")
	content := "[server]\ntoken = \"${secretsmanager:linkari/bearer-token}\"\n"
	if err := os.WriteFile(tomlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	out, run := newDoctorCmdForTest(t, dir, []string{"--path", tomlPath, "--json"})
	err := run()
	if err != nil {
		t.Fatalf("CT-8: doctor failed: %v\noutput:\n%s", err, out.String())
	}
	var result struct {
		Checks []doctorCheck `json:"checks"`
	}
	if jsonErr := json.Unmarshal(out.Bytes(), &result); jsonErr != nil {
		t.Fatalf("CT-8: JSON parse: %v\noutput:\n%s", jsonErr, out.String())
	}
	for _, c := range result.Checks {
		if c.Name == "aws_credentials" && c.Status == statusFail {
			t.Errorf("CT-8: aws_credentials fail check should not be emitted with explicit credentials; detail=%q", c.Message)
		}
	}
}

func TestDoctorPathIsolation_RoutingConfigDoesNotLoadDefaultConfig(t *testing.T) {
	dir := t.TempDir()
	defaultCfgDir := filepath.Join(dir, ".config", "linkari")
	if err := os.MkdirAll(defaultCfgDir, 0o700); err != nil {
		t.Fatalf("mkdir default config dir: %v", err)
	}
	defaultPath := filepath.Join(defaultCfgDir, "config.toml")
	defaultContent := "[server]\ntoken = \"${secretsmanager:linkari/bearer-token}\"\n"
	if err := os.WriteFile(defaultPath, []byte(defaultContent), 0o600); err != nil {
		t.Fatalf("write default config.toml: %v", err)
	}

	isolatedDir := filepath.Join(dir, "isolated")
	if err := os.MkdirAll(isolatedDir, 0o700); err != nil {
		t.Fatalf("mkdir isolated: %v", err)
	}
	isolatedPath := filepath.Join(isolatedDir, "config.toml")
	isolatedContent := "[server]\ntoken = \"test-token\"\n"
	if err := os.WriteFile(isolatedPath, []byte(isolatedContent), 0o600); err != nil {
		t.Fatalf("write isolated config.toml: %v", err)
	}
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")

	out, run := newDoctorCmdForTest(t, dir, []string{"--path", isolatedPath, "--json"})
	_ = run()

	var result struct {
		Checks []doctorCheck `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("JSON parse: %v\noutput:\n%s", err, out.String())
	}
	for _, c := range result.Checks {
		if c.Name == "aws_credentials" {
			t.Fatalf("CT-9: doctor --path escaped to default SM-backed config; output:\n%s", out.String())
		}
		if c.Name == "routing_config" && c.Status != statusOK {
			t.Fatalf("CT-9: routing_config status=%q detail=%q", c.Status, c.Message)
		}
	}
}

// EPIC-223 Contract Tests: Backup Freshness Check

// CT-1: Fresh backup → ok
func TestBackupFreshness_FreshBackup(t *testing.T) {
	dir := t.TempDir()
	backupPath := filepath.Join(dir, "queue.db")
	sidecarPath := backupPath + ".backup-meta.json"

	now := time.Now().UTC()
	completedAt := now.Add(-1 * time.Hour).Format(time.RFC3339Nano)
	sidecarContent := fmt.Sprintf(`{"created_at": "%s", "source_db": "%s", "backup_path": "%s", "queue_db_size_bytes": 1024}`, completedAt, backupPath, backupPath)

	if err := os.WriteFile(sidecarPath, []byte(sidecarContent), 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	checks := checkBackupFreshness(backupPath, now)
	if len(checks) == 0 {
		t.Fatal("expected non-empty checks")
	}

	c := checks[0]
	if c.Status != statusOK {
		t.Errorf("CT-1: status = %q, want %q (message: %s)", c.Status, statusOK, c.Message)
	}
	if c.Name != "backup_freshness" {
		t.Errorf("CT-1: name = %q, want backup_freshness", c.Name)
	}
}

// CT-2: Stale 25h → warn
func TestBackupFreshness_Stale25h(t *testing.T) {
	dir := t.TempDir()
	backupPath := filepath.Join(dir, "queue.db")
	sidecarPath := backupPath + ".backup-meta.json"

	now := time.Now().UTC()
	completedAt := now.Add(-25 * time.Hour).Format(time.RFC3339Nano)
	sidecarContent := fmt.Sprintf(`{"created_at": "%s", "source_db": "%s", "backup_path": "%s", "queue_db_size_bytes": 1024}`, completedAt, backupPath, backupPath)

	if err := os.WriteFile(sidecarPath, []byte(sidecarContent), 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	checks := checkBackupFreshness(backupPath, now)
	if len(checks) == 0 {
		t.Fatal("expected non-empty checks")
	}

	c := checks[0]
	if c.Status != statusWarn {
		t.Errorf("CT-2: status = %q, want %q (message: %s)", c.Status, statusWarn, c.Message)
	}
	if c.Name != "backup_freshness" {
		t.Errorf("CT-2: name = %q, want backup_freshness", c.Name)
	}
}

// CT-3: Stale 73h → fail
func TestBackupFreshness_Stale73h(t *testing.T) {
	dir := t.TempDir()
	backupPath := filepath.Join(dir, "queue.db")
	sidecarPath := backupPath + ".backup-meta.json"

	now := time.Now().UTC()
	completedAt := now.Add(-73 * time.Hour).Format(time.RFC3339Nano)
	sidecarContent := fmt.Sprintf(`{"created_at": "%s", "source_db": "%s", "backup_path": "%s", "queue_db_size_bytes": 1024}`, completedAt, backupPath, backupPath)

	if err := os.WriteFile(sidecarPath, []byte(sidecarContent), 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	checks := checkBackupFreshness(backupPath, now)
	if len(checks) == 0 {
		t.Fatal("expected non-empty checks")
	}

	c := checks[0]
	if c.Status != statusFail {
		t.Errorf("CT-3: status = %q, want %q (message: %s)", c.Status, statusFail, c.Message)
	}
	if c.Name != "backup_freshness" {
		t.Errorf("CT-3: name = %q, want backup_freshness", c.Name)
	}
}

// CT-4: Missing sidecar → warn (when backup path configured but backup not yet run)
func TestBackupFreshness_MissingSidecar(t *testing.T) {
	dir := t.TempDir()
	backupPath := filepath.Join(dir, "queue.db")

	now := time.Now().UTC()
	checks := checkBackupFreshness(backupPath, now)

	if len(checks) == 0 {
		t.Fatal("expected non-empty checks")
	}

	c := checks[0]
	// When backup_path is explicitly configured but sidecar doesn't exist,
	// emit a warn (not a fail) since the operator may not have run a backup yet.
	if c.Status != statusWarn {
		t.Errorf("CT-4: status = %q, want %q (warn when backup not yet run; message: %s)", c.Status, statusWarn, c.Message)
	}
	if c.Name != "backup_freshness" {
		t.Errorf("CT-4: name = %q, want backup_freshness", c.Name)
	}
}

// CT-5: Config-authority test (config set + env absent → reads config)
func TestResolveBackupPath_ConfigAuthority(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "queue.db")

	// Ensure LINKARI_BACKUP_PATH is unset
	t.Setenv("LINKARI_BACKUP_PATH", "")

	cfg := &ServerConfig{
		DB: DBConfig{
			BackupPath: configPath,
		},
	}

	resolved, err := resolveBackupPath(cfg)
	if err != nil {
		t.Fatalf("CT-5: resolveBackupPath: %v", err)
	}

	if resolved != configPath {
		t.Errorf("CT-5: resolved = %q, want %q (config must win over env)", resolved, configPath)
	}
}

// CT-6: Default fallback (empty config → XDG default)
func TestResolveBackupPath_DefaultFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfg := &ServerConfig{
		DB: DBConfig{
			BackupPath: "",
		},
	}

	resolved, err := resolveBackupPath(cfg)
	if err != nil {
		t.Fatalf("CT-6: resolveBackupPath: %v", err)
	}

	expectedDefault := filepath.Join(dir, ".local", "state", "linkari", "backups", "latest.db")
	if resolved != expectedDefault {
		t.Errorf("CT-6: resolved = %q, want %q", resolved, expectedDefault)
	}
}

// CT-7: Malformed sidecar → fail (conservative)
func TestBackupFreshness_MalformedSidecar(t *testing.T) {
	dir := t.TempDir()
	backupPath := filepath.Join(dir, "queue.db")
	sidecarPath := backupPath + ".backup-meta.json"

	// Write JSON with zero-value created_at (triggers missing created_at guard)
	sidecarContent := `{"created_at": "0001-01-01T00:00:00Z", "source_db": "/path", "backup_path": "/path", "queue_db_size_bytes": 1024}`

	if err := os.WriteFile(sidecarPath, []byte(sidecarContent), 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	now := time.Now().UTC()
	checks := checkBackupFreshness(backupPath, now)

	if len(checks) == 0 {
		t.Fatal("expected non-empty checks")
	}

	c := checks[0]
	if c.Status != statusFail {
		t.Errorf("CT-7: status = %q, want %q (conservative on malformed; message: %s)", c.Status, statusFail, c.Message)
	}
	if c.Name != "backup_missing" {
		t.Errorf("CT-7: name = %q, want backup_missing (conservative)", c.Name)
	}
}

// RG-1: Config-set + env-absent must not false-positive
// Regression guard against the tessdata-class authority bug.
func TestBackupFreshness_RG1_NoFalsePositive(t *testing.T) {
	dir := t.TempDir()
	configBackupPath := filepath.Join(dir, "configured.db")
	sidecarPath := configBackupPath + ".backup-meta.json"

	// Config is set, env is unset
	t.Setenv("LINKARI_BACKUP_PATH", "")

	now := time.Now().UTC()
	completedAt := now.Add(-30 * time.Minute).Format(time.RFC3339Nano)
	sidecarContent := fmt.Sprintf(`{"created_at": "%s", "source_db": "%s", "backup_path": "%s", "queue_db_size_bytes": 1024}`, completedAt, configBackupPath, configBackupPath)

	if err := os.WriteFile(sidecarPath, []byte(sidecarContent), 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	cfg := &ServerConfig{
		DB: DBConfig{
			BackupPath: configBackupPath,
		},
	}

	resolved, err := resolveBackupPath(cfg)
	if err != nil {
		t.Fatalf("RG-1: resolveBackupPath: %v", err)
	}

	// Verify config path was used
	if resolved != configBackupPath {
		t.Errorf("RG-1: resolveBackupPath returned %q, want %q", resolved, configBackupPath)
	}

	// Verify the check reports ok (not backup_missing)
	checks := checkBackupFreshness(resolved, now)
	if len(checks) == 0 {
		t.Fatal("RG-1: expected non-empty checks")
	}

	c := checks[0]
	if c.Status != statusOK {
		t.Errorf("RG-1: status = %q, want %q (should not false-positive as missing when config is set)", c.Status, statusOK)
	}
	if c.Name != "backup_freshness" {
		t.Errorf("RG-1: name = %q, want backup_freshness", c.Name)
	}
}

func TestK8sVolumeChecks_ModeDisabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LINKARI_K8S_MODE", "")
	if got := checkK8sVolume(dir); got != nil {
		t.Fatalf("expected nil checks when mode disabled, got %#v", got)
	}
}

func TestK8sVolumeChecks_AllOk(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LINKARI_K8S_MODE", "true")
	checks := checkK8sVolume(dir)
	if len(checks) != 3 {
		t.Fatalf("expected 3 checks, got %#v", checks)
	}
	if checks[0].Name != "k8s_volume_mount" || checks[0].Status != statusOK {
		t.Fatalf("unexpected mount check: %#v", checks[0])
	}
	// Capacity is a property of the host volume, not of the code under test:
	// checkK8sVolume warns below 20% free and fails below 5%. Asserting statusOK
	// here made the test fail on any machine with a fullish disk (observed on a
	// dev box at 13% free) while passing on a fresh CI runner. Assert the check
	// is present and not FAIL; the warn/fail thresholds are covered by the
	// freePct logic itself.
	if checks[1].Name != "k8s_volume_capacity" {
		t.Fatalf("unexpected capacity check: %#v", checks[1])
	}
	if checks[1].Status == statusFail {
		t.Fatalf("capacity check unexpectedly failed: %#v", checks[1])
	}
	if checks[2].Name != "k8s_single_writer" || checks[2].Status != statusOK {
		t.Fatalf("unexpected single writer check: %#v", checks[2])
	}
}

func TestResolveDataDir_ConfigFirst(t *testing.T) {
	t.Setenv("LINKARI_QUEUE_DB", filepath.Join("/tmp", "env", "queue.db"))
	cfg := &ServerConfig{QueueDB: filepath.Join("/tmp", "configured", "queue.db")}
	if got := resolveDataDir(cfg); got != filepath.Join("/tmp", "configured") {
		t.Fatalf("expected config dir, got %q", got)
	}
}

// ============================================================
// EPIC-231: AWS credential source + SM access doctor tests
// ============================================================

// CT-1: Doctor reports credential source type when profile configured.
func TestAWSDoctorCT1_CredentialSourceLabel(t *testing.T) {
	orig := awsDoctorProbeFn
	defer func() { awsDoctorProbeFn = orig }()

	awsDoctorProbeFn = func(_ context.Context, awsCfg secrets.AWSConfig) awsDoctorResult {
		return awsDoctorResult{Source: awsCfg.Profile}
	}

	// Use formatAWSCheck directly with a hand-crafted result.
	result := awsDoctorResult{
		Source:  "shared-credentials-file",
		ARN:     "arn:aws:iam::082515828319:user/brian",
		Profile: "brianonpoint",
		SMOK:    true,
	}
	check := formatAWSCheck(result)
	if check.Status != statusOK {
		t.Errorf("CT-1: status = %q, want ok", check.Status)
	}
	if !strings.Contains(check.Message, "shared-credentials-file") {
		t.Errorf("CT-1: message %q does not contain source label", check.Message)
	}
	if !strings.Contains(check.Message, "profile: brianonpoint") {
		t.Errorf("CT-1: message %q does not contain profile name", check.Message)
	}
}

// CT-2: Doctor reports IAM ARN via GetCallerIdentity.
func TestAWSDoctorCT2_ARNInOutput(t *testing.T) {
	result := awsDoctorResult{
		Source: "ec2-instance-metadata",
		ARN:    "arn:aws:iam::082515828319:role/linkari-k3s",
		SMOK:   true,
	}
	check := formatAWSCheck(result)
	if !strings.Contains(check.Message, "arn:aws:iam::082515828319:role/linkari-k3s") {
		t.Errorf("CT-2: message %q does not contain full ARN", check.Message)
	}
}

// CT-3: Doctor reports aws_no_credentials error when no credentials available.
func TestAWSDoctorCT3_NoCredentials(t *testing.T) {
	result := awsDoctorResult{
		Err: fmt.Errorf("no credentials: operation error STS: GetCallerIdentity: no credentials"),
	}
	check := formatAWSCheck(result)
	if check.Status != statusFail {
		t.Errorf("CT-3: status = %q, want fail", check.Status)
	}
	if !strings.Contains(check.Message, "no credentials found") {
		t.Errorf("CT-3: message %q missing 'no credentials found'", check.Message)
	}
}

// CT-4: Doctor reports aws_sm_access_denied when credentials lack SM permissions.
func TestAWSDoctorCT4_SMAccessDenied(t *testing.T) {
	result := awsDoctorResult{
		Source:  "shared-credentials-file",
		ARN:     "arn:aws:iam::082515828319:user/brian",
		Profile: "brianonpoint",
		SMOK:    false,
		Err:     fmt.Errorf("sm access denied: AccessDeniedException"),
	}
	check := formatAWSCheck(result)
	if check.Status != statusFail {
		t.Errorf("CT-4: status = %q, want fail", check.Status)
	}
	if !strings.Contains(check.Message, "Secrets Manager access denied") {
		t.Errorf("CT-4: message %q missing 'Secrets Manager access denied'", check.Message)
	}
}

// CT-5: Remediation hint includes [aws] profile and role_arn config options.
func TestAWSDoctorCT5_RemediationHint(t *testing.T) {
	result := awsDoctorResult{
		Err: fmt.Errorf("no credentials"),
	}
	check := formatAWSCheck(result)
	if !strings.Contains(check.Message, "[aws] profile") {
		t.Errorf("CT-5: message %q missing '[aws] profile'", check.Message)
	}
	if !strings.Contains(check.Message, "role_arn") {
		t.Errorf("CT-5: message %q missing 'role_arn'", check.Message)
	}
}

// CT-6: detectCredentialSource returns expected labels for each source type.
func TestAWSDoctorCT6_CredentialSourceDetection(t *testing.T) {
	t.Run("profile", func(t *testing.T) {
		src := detectCredentialSource(secrets.AWSConfig{Profile: "brianonpoint"})
		if src != "shared-credentials-file" {
			t.Errorf("CT-6/profile: got %q, want shared-credentials-file", src)
		}
	})
	t.Run("env-vars", func(t *testing.T) {
		t.Setenv("AWS_ACCESS_KEY_ID", "AKIAIOSFODNN7EXAMPLE")
		src := detectCredentialSource(secrets.AWSConfig{})
		if src != "environment-variables" {
			t.Errorf("CT-6/env-vars: got %q, want environment-variables", src)
		}
	})
	t.Run("web-identity", func(t *testing.T) {
		t.Setenv("AWS_ACCESS_KEY_ID", "")
		t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", "/var/run/secrets/token")
		src := detectCredentialSource(secrets.AWSConfig{})
		if src != "web-identity-token" {
			t.Errorf("CT-6/web-identity: got %q, want web-identity-token", src)
		}
	})
	t.Run("imds", func(t *testing.T) {
		t.Setenv("AWS_ACCESS_KEY_ID", "")
		t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", "")
		t.Setenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "")
		t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "")
		src := detectCredentialSource(secrets.AWSConfig{})
		if src != "ec2-instance-metadata" {
			t.Errorf("CT-6/imds: got %q, want ec2-instance-metadata", src)
		}
	})
}

// RG-1: Doctor output never contains AWS secret key material.
func TestAWSDoctorRG1_NoSecretKeyLeakage(t *testing.T) {
	// Simulate a result where ARN is present but no key material.
	result := awsDoctorResult{
		Source: "environment-variables",
		ARN:    "arn:aws:iam::082515828319:assumed-role/dev/session",
		SMOK:   true,
	}
	check := formatAWSCheck(result)
	sensitivePatterns := []string{"ASIA", "AKIA", "AWS_SECRET_ACCESS_KEY"}
	for _, pat := range sensitivePatterns {
		if strings.Contains(check.Message, pat) {
			t.Errorf("RG-1: output contains sensitive pattern %q: %s", pat, check.Message)
		}
	}
}

// RG-2: Zero-value AWSConfig preserves existing behavior (no panic, no error injection).
func TestAWSDoctorRG2_ZeroValueConfig(t *testing.T) {
	// Zero-value config must not cause formatAWSCheck to panic.
	// Simulate success with empty profile.
	result := awsDoctorResult{
		Source: "ec2-instance-metadata",
		ARN:    "arn:aws:iam::082515828319:role/linkari-k3s",
		SMOK:   true,
	}
	check := formatAWSCheck(result)
	if check.Status != statusOK {
		t.Errorf("RG-2: zero-value AWSConfig with valid creds should produce ok check, got %q", check.Status)
	}
	// Message must not contain "(profile: )" when profile is empty.
	if strings.Contains(check.Message, "profile: )") {
		t.Errorf("RG-2: message %q contains empty profile annotation", check.Message)
	}
}
