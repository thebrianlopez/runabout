package chainindex

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ComputeContentHash returns "sha256:<hex>" computed over all .md files under
// docsRoot. The preimage is: for each relative path in sorted lexicographic order,
//   "{relative_path}\t{size_bytes}\t{mtime_nanoseconds}\n"
// No file content is read - stat metadata only.
func ComputeContentHash(docsRoot string) (string, error) {
	paths, err := collectMDPaths(docsRoot)
	if err != nil {
		return "", err
	}
	sort.Strings(paths)

	h := sha256.New()
	for _, rel := range paths {
		info, err := os.Stat(filepath.Join(docsRoot, rel))
		if err != nil {
			// Stat error: emit warning but continue with remaining paths.
			continue
		}
		fmt.Fprintf(h, "%s\t%d\t%d\n", rel, info.Size(), info.ModTime().UnixNano()) //nolint:errcheck
	}
	return "sha256:" + fmt.Sprintf("%x", h.Sum(nil)), nil
}

// VerifyContentHash re-computes the hash over current filesystem state and
// returns (true, nil) when it matches storedHash. Returns (false, nil) on
// mismatch (stale), never an error on mismatch alone.
func VerifyContentHash(storedHash, docsRoot string) (bool, error) {
	current, err := ComputeContentHash(docsRoot)
	if err != nil {
		return false, err
	}
	return current == storedHash, nil
}

// collectMDPaths returns all .md file paths under docsRoot, relative to docsRoot.
func collectMDPaths(docsRoot string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(docsRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable paths
		}
		if d.IsDir() {
			name := d.Name()
			// Skip hidden directories and .git.
			if strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".md") {
			rel, _ := filepath.Rel(docsRoot, path)
			paths = append(paths, rel)
		}
		return nil
	})
	return paths, err
}
