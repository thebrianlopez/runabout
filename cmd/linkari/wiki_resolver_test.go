package main

import (
	"os"
	"path/filepath"
	"testing"
)

// helpers

func makeTopicDir(t *testing.T, root, dirName, indexFile string) string {
	t.Helper()
	dir := filepath.Join(root, dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if indexFile != "" {
		if err := os.WriteFile(filepath.Join(dir, indexFile), []byte("# index\n"), 0o600); err != nil {
			t.Fatalf("write index: %v", err)
		}
	}
	return dir
}

func wikiCfg(root string, profiles []string) WikiConfig {
	return WikiConfig{
		Enabled:          true,
		RootPath:         root,
		WikiSubdir:       "",
		Profiles:         profiles,
		MaxContextTokens: 500,
		IndexFilename:    "_index.md",
	}
}

// CT-1: NewWikiTopicResolver returns nil when Enabled=false.
func TestWikiResolver_CT1_NilWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	cfg := wikiCfg(dir, []string{"eng"})
	cfg.Enabled = false
	if r := NewWikiTopicResolver(cfg); r != nil {
		t.Errorf("CT-1: expected nil resolver when Enabled=false, got non-nil")
	}
}

// CT-2: NewWikiTopicResolver returns nil when vault root is missing.
func TestWikiResolver_CT2_NilWhenVaultMissing(t *testing.T) {
	cfg := wikiCfg("/tmp/linkari-nonexistent-vault-ct2", []string{"eng"})
	if r := NewWikiTopicResolver(cfg); r != nil {
		t.Errorf("CT-2: expected nil resolver when vault root missing, got non-nil")
	}
}

// CT-3: Resolve returns ("", false) when profile is not in cfg.Profiles.
func TestWikiResolver_CT3_ProfileGateMiss(t *testing.T) {
	root := t.TempDir()
	makeTopicDir(t, root, "golang", "_index.md")
	r := NewWikiTopicResolver(wikiCfg(root, []string{"eng"}))
	if r == nil {
		t.Fatal("CT-3: resolver is nil")
	}
	path, found := r.Resolve("other", []string{"golang"})
	if found || path != "" {
		t.Errorf("CT-3: profile gate should block; got path=%q found=%v", path, found)
	}
}

// CT-4: Resolve returns (indexPath, true) when tag matches topic directory.
func TestWikiResolver_CT4_TagMatchReturnsIndexPath(t *testing.T) {
	root := t.TempDir()
	makeTopicDir(t, root, "golang", "_index.md")
	r := NewWikiTopicResolver(wikiCfg(root, []string{"eng"}))
	if r == nil {
		t.Fatal("CT-4: resolver is nil")
	}
	path, found := r.Resolve("eng", []string{"golang"})
	if !found {
		t.Errorf("CT-4: expected match for tag 'golang', not found")
	}
	want := filepath.Join(root, "golang", "_index.md")
	if path != want {
		t.Errorf("CT-4: path = %q, want %q", path, want)
	}
}

// CT-5: normalizeTopicName replaces underscores with hyphens.
func TestWikiResolver_CT5_NormalizeUnderscoreToHyphen(t *testing.T) {
	got := normalizeTopicName("ai_tools")
	if got != "ai-tools" {
		t.Errorf("CT-5: normalize(%q) = %q, want %q", "ai_tools", got, "ai-tools")
	}
}

// CT-6: normalizeTopicName lowercases the input.
func TestWikiResolver_CT6_NormalizeLowercase(t *testing.T) {
	got := normalizeTopicName("GoLang")
	if got != "golang" {
		t.Errorf("CT-6: normalize(%q) = %q, want %q", "GoLang", got, "golang")
	}
}

// CT-7: Resolve matches a normalized directory name (tag "AI_Tools" → dir "ai-tools").
func TestWikiResolver_CT7_NormalizedDirMatch(t *testing.T) {
	root := t.TempDir()
	makeTopicDir(t, root, "ai-tools", "_index.md")
	r := NewWikiTopicResolver(wikiCfg(root, []string{"eng"}))
	if r == nil {
		t.Fatal("CT-7: resolver is nil")
	}
	path, found := r.Resolve("eng", []string{"AI_Tools"})
	if !found {
		t.Errorf("CT-7: normalized dir match failed for tag 'AI_Tools' → 'ai-tools'")
	}
	want := filepath.Join(root, "ai-tools", "_index.md")
	if path != want {
		t.Errorf("CT-7: path = %q, want %q", path, want)
	}
}

// CT-8: Resolve skips a matching directory when the index file is absent.
func TestWikiResolver_CT8_SkipsDirWithoutIndexFile(t *testing.T) {
	root := t.TempDir()
	makeTopicDir(t, root, "golang", "") // no index file
	r := NewWikiTopicResolver(wikiCfg(root, []string{"eng"}))
	if r == nil {
		t.Fatal("CT-8: resolver is nil")
	}
	path, found := r.Resolve("eng", []string{"golang"})
	if found || path != "" {
		t.Errorf("CT-8: should not match dir without index; got path=%q found=%v", path, found)
	}
}

// CT-9: Resolve returns the first matching tag (not the last).
func TestWikiResolver_CT9_FirstMatchWins(t *testing.T) {
	root := t.TempDir()
	makeTopicDir(t, root, "first", "_index.md")
	makeTopicDir(t, root, "second", "_index.md")
	r := NewWikiTopicResolver(wikiCfg(root, []string{"eng"}))
	if r == nil {
		t.Fatal("CT-9: resolver is nil")
	}
	path, found := r.Resolve("eng", []string{"first", "second"})
	if !found {
		t.Fatal("CT-9: expected a match")
	}
	want := filepath.Join(root, "first", "_index.md")
	if path != want {
		t.Errorf("CT-9: path = %q, want first match %q", path, want)
	}
}
