package chainindex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeCorpus writes n .md files to dir and returns their paths.
func makeCorpus(t *testing.T, dir string, n int) []string {
	t.Helper()
	paths := make([]string, n)
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("artifact_%03d.md", i))
		if err := os.WriteFile(p, []byte("# Artifact\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		paths[i] = p
	}
	return paths
}

// CT-7: Two consecutive ComputeContentHash calls on unchanged corpus produce identical hex.
func TestComputeContentHash_Deterministic(t *testing.T) {
	dir := t.TempDir()
	makeCorpus(t, dir, 5)

	h1, err := ComputeContentHash(dir)
	if err != nil {
		t.Fatalf("CT-7: first hash error: %v", err)
	}
	h2, err := ComputeContentHash(dir)
	if err != nil {
		t.Fatalf("CT-7: second hash error: %v", err)
	}
	if h1 != h2 {
		t.Errorf("CT-7: hash not deterministic: %q vs %q", h1, h2)
	}
}

// CT-8: Empty corpus returns a well-defined sha256: string (not panic, not empty).
func TestComputeContentHash_EmptyCorpus(t *testing.T) {
	dir := t.TempDir()
	h, err := ComputeContentHash(dir)
	if err != nil {
		t.Fatalf("CT-8: unexpected error: %v", err)
	}
	if !strings.HasPrefix(h, "sha256:") {
		t.Errorf("CT-8: expected sha256: prefix, got: %q", h)
	}
	if len(h) == len("sha256:") {
		t.Error("CT-8: hash should not be empty after sha256: prefix")
	}
}

// CT-1: Touching an artifact mtime changes the computed hash.
func TestComputeContentHash_MtimeChange(t *testing.T) {
	dir := t.TempDir()
	paths := makeCorpus(t, dir, 3)

	h1, err := ComputeContentHash(dir)
	if err != nil {
		t.Fatalf("CT-1: initial hash error: %v", err)
	}

	// Touch one file's mtime.
	future := time.Now().Add(5 * time.Second)
	if err := os.Chtimes(paths[0], future, future); err != nil {
		t.Fatalf("CT-1: chtimes error: %v", err)
	}

	h2, err := ComputeContentHash(dir)
	if err != nil {
		t.Fatalf("CT-1: second hash error: %v", err)
	}
	if h1 == h2 {
		t.Error("CT-1: expected hash to change after mtime update")
	}
}

// CT-2: Adding a new artifact changes the computed hash.
func TestComputeContentHash_AddArtifact(t *testing.T) {
	dir := t.TempDir()
	makeCorpus(t, dir, 3)

	h1, err := ComputeContentHash(dir)
	if err != nil {
		t.Fatalf("CT-2: initial hash error: %v", err)
	}

	// Add one more artifact.
	if err := os.WriteFile(filepath.Join(dir, "new_artifact.md"), []byte("# New\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h2, err := ComputeContentHash(dir)
	if err != nil {
		t.Fatalf("CT-2: second hash error: %v", err)
	}
	if h1 == h2 {
		t.Error("CT-2: expected hash to change after adding artifact")
	}
}

// CT-3: Deleting an artifact changes the computed hash.
func TestComputeContentHash_DeleteArtifact(t *testing.T) {
	dir := t.TempDir()
	paths := makeCorpus(t, dir, 3)

	h1, err := ComputeContentHash(dir)
	if err != nil {
		t.Fatalf("CT-3: initial hash error: %v", err)
	}

	if err := os.Remove(paths[0]); err != nil {
		t.Fatalf("CT-3: remove error: %v", err)
	}

	h2, err := ComputeContentHash(dir)
	if err != nil {
		t.Fatalf("CT-3: second hash error: %v", err)
	}
	if h1 == h2 {
		t.Error("CT-3: expected hash to change after deleting artifact")
	}
}

// CT-4: No changes - VerifyContentHash returns (true, nil).
func TestVerifyContentHash_NoChange(t *testing.T) {
	dir := t.TempDir()
	makeCorpus(t, dir, 5)

	h, err := ComputeContentHash(dir)
	if err != nil {
		t.Fatalf("CT-4: compute error: %v", err)
	}

	ok, err := VerifyContentHash(h, dir)
	if err != nil {
		t.Fatalf("CT-4: verify error: %v", err)
	}
	if !ok {
		t.Error("CT-4: expected (true, nil) on unchanged corpus")
	}
}

// CT-5: Staleness check completes in <100ms on a corpus of 300 artifacts.
func TestComputeContentHash_Performance(t *testing.T) {
	dir := t.TempDir()
	makeCorpus(t, dir, 300)

	start := time.Now()
	_, err := ComputeContentHash(dir)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("CT-5: unexpected error: %v", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("CT-5: expected <100ms, took %v", elapsed)
	}
}

// CT-6: Stale index returns (false, nil) - not an error.
func TestVerifyContentHash_Stale(t *testing.T) {
	dir := t.TempDir()
	paths := makeCorpus(t, dir, 3)

	h, err := ComputeContentHash(dir)
	if err != nil {
		t.Fatalf("CT-6: compute error: %v", err)
	}

	// Modify one file to make the stored hash stale.
	future := time.Now().Add(10 * time.Second)
	if err := os.Chtimes(paths[1], future, future); err != nil {
		t.Fatalf("CT-6: chtimes error: %v", err)
	}

	ok, err := VerifyContentHash(h, dir)
	if err != nil {
		t.Fatalf("CT-6: expected nil error on mismatch, got: %v", err)
	}
	if ok {
		t.Error("CT-6: expected (false, nil) on stale corpus")
	}
}
