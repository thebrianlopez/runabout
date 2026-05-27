package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// CT-1: [wiki] absent from config.toml → Wiki.Enabled=false, Validate() is a no-op.
func TestWikiConfig_AbsentBlock_NoOp(t *testing.T) {
	var cfg ServerConfig
	if _, err := toml.Decode("[server]\ntoken = \"tok\"\n", &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Wiki.Enabled {
		t.Errorf("Wiki.Enabled = true, want false when [wiki] block is absent")
	}
	if err := cfg.Wiki.Validate(); err != nil {
		t.Errorf("Validate() on absent wiki block = %v, want nil", err)
	}
}

// CT-2: [wiki] present with all valid fields → Validate() returns nil.
func TestWikiConfig_ValidConfig_PassesValidation(t *testing.T) {
	dir := t.TempDir()
	tomlStr := `[wiki]
enabled = true
root_path = "` + dir + `"
profiles = ["eng"]
max_context_tokens = 800
index_filename = "_index.md"
`
	var cfg struct {
		Wiki WikiConfig `toml:"wiki"`
	}
	if _, err := toml.Decode(tomlStr, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !cfg.Wiki.Enabled {
		t.Fatalf("Wiki.Enabled = false after decode, want true")
	}
	if err := cfg.Wiki.Validate(); err != nil {
		t.Errorf("CT-2: Validate() = %v, want nil", err)
	}
}

// CT-3: Enabled but root_path empty → Validate() returns WikiConfigWarning (non-fatal).
func TestWikiConfig_EmptyRootPath_ReturnsWarning(t *testing.T) {
	cfg := WikiConfig{
		Enabled:          true,
		RootPath:         "",
		Profiles:         []string{"eng"},
		MaxContextTokens: 500,
		IndexFilename:    "_index.md",
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("CT-3: Validate() = nil, want WikiConfigWarning")
	}
	var w WikiConfigWarning
	if !errors.As(err, &w) {
		t.Errorf("CT-3: error type = %T, want WikiConfigWarning; err=%v", err, err)
	}
}

// CT-4: root_path set but directory does not exist → WikiConfigWarning.
func TestWikiConfig_MissingVaultDir_ReturnsWarning(t *testing.T) {
	cfg := WikiConfig{
		Enabled:          true,
		RootPath:         "/no/such/vault/path",
		Profiles:         []string{"eng"},
		MaxContextTokens: 500,
		IndexFilename:    "_index.md",
	}
	err := cfg.Validate()
	var w WikiConfigWarning
	if !errors.As(err, &w) {
		t.Errorf("CT-4: error type = %T %v, want WikiConfigWarning", err, err)
	}
	if !strings.Contains(w.Msg, "wiki_root_missing") {
		t.Errorf("CT-4: warning msg %q missing wiki_root_missing prefix", w.Msg)
	}
}

// CT-5: max_context_tokens > 2000 → hard error (not a warning).
func TestWikiConfig_MaxTokensExceedsLimit_HardError(t *testing.T) {
	dir := t.TempDir()
	cfg := WikiConfig{
		Enabled:          true,
		RootPath:         dir,
		Profiles:         []string{"eng"},
		MaxContextTokens: 2001,
		IndexFilename:    "_index.md",
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("CT-5: Validate() = nil, want error")
	}
	var w WikiConfigWarning
	if errors.As(err, &w) {
		t.Errorf("CT-5: got WikiConfigWarning (non-fatal), want hard error; err=%v", err)
	}
}

// CT-6: profiles empty → hard error.
func TestWikiConfig_EmptyProfiles_HardError(t *testing.T) {
	dir := t.TempDir()
	cfg := WikiConfig{
		Enabled:          true,
		RootPath:         dir,
		Profiles:         nil,
		MaxContextTokens: 500,
		IndexFilename:    "_index.md",
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("CT-6: Validate() = nil, want error for empty profiles")
	}
	var w WikiConfigWarning
	if errors.As(err, &w) {
		t.Errorf("CT-6: got non-fatal warning, want hard error for empty profiles")
	}
}

// CT-7: index_filename empty → hard error.
func TestWikiConfig_EmptyIndexFilename_HardError(t *testing.T) {
	dir := t.TempDir()
	cfg := WikiConfig{
		Enabled:          true,
		RootPath:         dir,
		Profiles:         []string{"eng"},
		MaxContextTokens: 500,
		IndexFilename:    "",
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("CT-7: Validate() = nil, want error for empty index_filename")
	}
	var w WikiConfigWarning
	if errors.As(err, &w) {
		t.Errorf("CT-7: got non-fatal warning, want hard error for empty index_filename")
	}
}

// CT-8a: TopicRootPath() returns RootPath/WikiSubdir when WikiSubdir is set.
// CT-8b: TopicRootPath() returns RootPath when WikiSubdir is empty.
func TestWikiConfig_TopicRootPath(t *testing.T) {
	root := "/vault/root"

	// subdir set
	cfgSub := WikiConfig{RootPath: root, WikiSubdir: "wiki"}
	want := filepath.Join(root, "wiki")
	if got := cfgSub.TopicRootPath(); got != want {
		t.Errorf("CT-8a: TopicRootPath() = %q, want %q", got, want)
	}

	// subdir empty  -  topics are flat at root
	cfgFlat := WikiConfig{RootPath: root, WikiSubdir: ""}
	if got := cfgFlat.TopicRootPath(); got != root {
		t.Errorf("CT-8b: TopicRootPath() = %q, want %q", got, root)
	}
}

// Doctor integration: enabled wiki block with valid vault → doctor emits ok check with topic count.
func TestDoctorWiki_ValidVault_OkCheck(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "linkari")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Create a vault with 2 topic dirs.
	vaultDir := filepath.Join(dir, "vault")
	for _, topic := range []string{"ai", "go-lang"} {
		if err := os.MkdirAll(filepath.Join(vaultDir, topic), 0o700); err != nil {
			t.Fatalf("mkdir topic: %v", err)
		}
	}

	tomlPath := filepath.Join(cfgDir, "config.toml")
	content := "[server]\ntoken = \"tok\"\n\n[server.wiki]\nenabled = true\nroot_path = \"" + vaultDir + "\"\nprofiles = [\"eng\"]\nmax_context_tokens = 800\nindex_filename = \"_index.md\"\n"
	if err := os.WriteFile(tomlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	out, run := newDoctorCmdForTest(t, dir, []string{"--path", tomlPath, "--json"})
	_ = run()

	var result struct {
		Checks []doctorCheck `json:"checks"`
	}
	if err := decodeJSONBytes(t, out.Bytes(), &result); err != nil {
		t.Fatalf("JSON parse: %v\noutput:\n%s", err, out.String())
	}
	found := false
	for _, c := range result.Checks {
		if c.Name == "wiki" {
			found = true
			if c.Status != statusOK {
				t.Errorf("wiki check status = %q, want ok; msg=%q", c.Status, c.Message)
			}
			if !strings.Contains(c.Message, "topics=2") {
				t.Errorf("wiki check msg %q should contain topics=2", c.Message)
			}
		}
	}
	if !found {
		t.Errorf("wiki check not present in doctor output")
	}
}

func decodeJSONBytes(t *testing.T, b []byte, v any) error {
	t.Helper()
	return json.Unmarshal(b, v)
}
