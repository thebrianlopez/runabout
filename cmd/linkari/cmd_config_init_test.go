package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// referenceServerConfig pins the expected field values in the generated template.
// Secret fields hold the raw ${secretsmanager:...} ref strings (not resolved values).
var referenceServerConfig = ServerConfig{
	Token:          "${secretsmanager:linkari/bearer-token}",
	TSNetAuthKey:   "${secretsmanager:linkari/tsnet-authkey}",
	FirebaseSA:     "${secretsmanager:linkari/firebase-sa}",
	NotifyMinScore: 10,
	Port:           8080,
	TsnetHostname:  "linkari",
}

// pinned reference *bool for tsnet field.
var referenceTsnetTrue = true

// TestConfigInit_RoundTrip parses the generated template and checks it round-trips
// into a ServerConfig with all populated fields matching the pinned reference.
func TestConfigInit_RoundTrip(t *testing.T) {
	var cfg Config
	if _, err := toml.Decode(serverYAMLTemplate, &cfg); err != nil {
		t.Fatalf("template parse error: %v", err)
	}
	got := cfg.Server

	// Secret fields (raw refs, not resolved).
	if got.Token != referenceServerConfig.Token {
		t.Errorf("Token = %q, want %q", got.Token, referenceServerConfig.Token)
	}
	if got.TSNetAuthKey != referenceServerConfig.TSNetAuthKey {
		t.Errorf("TSNetAuthKey = %q, want %q", got.TSNetAuthKey, referenceServerConfig.TSNetAuthKey)
	}
	if got.FirebaseSA != referenceServerConfig.FirebaseSA {
		t.Errorf("FirebaseSA = %q, want %q", got.FirebaseSA, referenceServerConfig.FirebaseSA)
	}

	// Non-secret defaults.
	if got.NotifyMinScore != referenceServerConfig.NotifyMinScore {
		t.Errorf("NotifyMinScore = %d, want %d", got.NotifyMinScore, referenceServerConfig.NotifyMinScore)
	}
	if got.Port != referenceServerConfig.Port {
		t.Errorf("Port = %d, want %d", got.Port, referenceServerConfig.Port)
	}
	if got.TsnetHostname != referenceServerConfig.TsnetHostname {
		t.Errorf("TsnetHostname = %q, want %q", got.TsnetHostname, referenceServerConfig.TsnetHostname)
	}

	// Tsnet *bool: template sets tsnet = true → pointer to true.
	if got.Tsnet == nil {
		t.Error("Tsnet = nil, want *true")
	} else if *got.Tsnet != referenceTsnetTrue {
		t.Errorf("*Tsnet = %v, want true", *got.Tsnet)
	}

	// Debug defaults to false.
	if got.Debug {
		t.Error("Debug = true, want false")
	}
}

// TestConfigInit_WritesFile covers the default write path.
func TestConfigInit_WritesFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")

	cmd := configInitCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--path", target})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != serverYAMLTemplate {
		t.Errorf("file content mismatch\ngot:\n%s\nwant:\n%s", data, serverYAMLTemplate)
	}
	if !strings.Contains(out.String(), "written to") {
		t.Errorf("expected written message, got: %q", out.String())
	}

	// File permissions must be 0600.
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0600", fi.Mode().Perm())
	}
}

// TestConfigInit_Idempotent verifies no-op when file exists without --force.
func TestConfigInit_Idempotent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")

	existing := "[server]\nport = 9999\n"
	if err := os.WriteFile(target, []byte(existing), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	cmd := configInitCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--path", target})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != existing {
		t.Error("file was modified without --force")
	}
	if !strings.Contains(out.String(), "already exists") {
		t.Errorf("expected 'already exists' message, got: %q", out.String())
	}
}

// TestConfigInit_Force verifies the file is overwritten and a backup created.
func TestConfigInit_Force(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")

	existing := "[server]\nport = 9999\n"
	if err := os.WriteFile(target, []byte(existing), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	cmd := configInitCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--path", target, "--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Original content replaced.
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) == existing {
		t.Error("file was not overwritten with --force")
	}

	// A backup file must exist containing the original content.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var backupFound bool
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "config.toml.backup-") {
			backupFound = true
			backupData, _ := os.ReadFile(filepath.Join(dir, e.Name()))
			if string(backupData) != existing {
				t.Errorf("backup content mismatch: %q", backupData)
			}
		}
	}
	if !backupFound {
		t.Error("no backup file created with --force")
	}
	if !strings.Contains(out.String(), "backed up") {
		t.Errorf("expected backup message, got: %q", out.String())
	}
}

// TestConfigInit_DryRun verifies stdout output and no disk write.
func TestConfigInit_DryRun(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")

	cmd := configInitCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--path", target, "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("--dry-run wrote to disk")
	}
	if out.String() != serverYAMLTemplate {
		t.Errorf("dry-run stdout mismatch\ngot:\n%s\nwant:\n%s", out.String(), serverYAMLTemplate)
	}
}

// TestConfigInit_CreatesParentDir verifies missing parent directories are created.
func TestConfigInit_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "deep", "config.toml")

	cmd := configInitCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--path", target})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if _, err := os.Stat(target); err != nil {
		t.Errorf("file not created: %v", err)
	}

	// Parent directory permissions must be 0700.
	parent := filepath.Dir(target)
	fi, err := os.Stat(parent)
	if err != nil {
		t.Fatalf("Stat parent: %v", err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("parent mode = %o, want 0700", fi.Mode().Perm())
	}
}
