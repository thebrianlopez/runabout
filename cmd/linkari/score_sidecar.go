package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// scoreJSON is the on-disk schema for _score.json files written by the scoring
// pipeline. Defined here so both cmd_backfill.go and the watchdog rescue path
// share a single source of truth for the on-disk format.
type scoreJSON struct {
	Score    int    `json:"score"`
	Verdict  string `json:"verdict"`
	Slug     string `json:"slug"`
	Profile  string `json:"profile"`
	URL      string `json:"url"`
	ScoredAt string `json:"scored_at"`
	Tags     string `json:"tags,omitempty"`
}

// scoreIndexKey is the composite key for the on-disk index, honoring the
// EPIC-054 workspace key invariant: same URL under different profiles produces
// distinct workspace dirs and distinct _score.json files.
type scoreIndexKey struct{ URL, Profile string }

// buildScoreIndex walks urlWorkDir (max depth 2 — root → workspace → file)
// and returns a map of (url, profile) → scoreJSON for every valid _score.json
// found. Symlinks are resolved; any entry whose resolved path escapes urlWorkDir
// is silently skipped (traversal guard). Returns nil when urlWorkDir does not
// exist.
//
// The index is built fresh on each sweep that has stuck rows — no cross-tick
// caching. At ~100 workspace dirs a single WalkDir + readFile pass completes in
// well under 200ms on SSD. The zero-IO fast-path (no stuck rows) skips the
// walk entirely.
func buildScoreIndex(urlWorkDir string) (map[scoreIndexKey]scoreJSON, error) {
	rootAbs, err := filepath.Abs(urlWorkDir)
	if err != nil {
		return nil, err
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	rootPrefix := rootResolved + string(os.PathSeparator)

	index := make(map[scoreIndexKey]scoreJSON)
	err = filepath.WalkDir(rootAbs, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil // skip unreadable entries
		}
		rel, _ := filepath.Rel(rootAbs, path)
		depth := strings.Count(rel, string(os.PathSeparator))
		if d.IsDir() {
			if depth == 0 {
				return nil // the root itself — continue
			}
			if depth >= 2 {
				return fs.SkipDir // don't recurse beyond one workspace level
			}
			return nil
		}
		// Only _score.json files exactly one level deep.
		if d.Name() != "_score.json" || depth != 1 {
			return nil
		}
		// Traversal guard: resolve symlinks and ensure the file stays inside root.
		resolved, serr := filepath.EvalSymlinks(path)
		if serr != nil {
			return nil
		}
		if !strings.HasPrefix(resolved, rootPrefix) {
			return nil // symlink escape — refuse
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		var s scoreJSON
		if jerr := json.Unmarshal(data, &s); jerr != nil || s.URL == "" {
			return nil
		}
		key := scoreIndexKey{URL: s.URL, Profile: s.Profile}
		index[key] = s
		return nil
	})
	return index, err
}
