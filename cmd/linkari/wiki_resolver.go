package main

import (
	"os"
	"path/filepath"
	"strings"
)

// WikiTopicResolver resolves a (profile, tags) pair to an Obsidian topic index
// file path. Nil resolver is a no-op; callers must nil-check before use.
// EPIC-180 M2.
type WikiTopicResolver struct {
	cfg WikiConfig
}

// NewWikiTopicResolver returns nil when cfg.Enabled is false or the vault root
// is absent or inaccessible. All other Validate errors (hard config errors)
// also return nil - the caller's doctor check catches those separately.
func NewWikiTopicResolver(cfg WikiConfig) *WikiTopicResolver {
	if !cfg.Enabled {
		return nil
	}
	if cfg.RootPath == "" {
		return nil
	}
	fi, err := os.Stat(cfg.RootPath)
	if err != nil || !fi.IsDir() {
		return nil
	}
	return &WikiTopicResolver{cfg: cfg}
}

// normalizeTopicName converts a tag to the canonical directory name form:
// lowercased with underscores replaced by hyphens.
func normalizeTopicName(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "_", "-")
}

// Resolve returns the index file path for the first tag that maps to an
// existing topic directory under TopicRootPath. Profile gate is checked first;
// tags are iterated in order; first hit wins.
//
// Returns ("", false) when no match is found (profile gate miss, no matching
// dir, or index file absent in matched dir).
func (r *WikiTopicResolver) Resolve(profile string, tags []string) (string, bool) {
	profileMatched := false
	for _, p := range r.cfg.Profiles {
		if p == profile {
			profileMatched = true
			break
		}
	}
	if !profileMatched {
		return "", false
	}

	topicRoot := r.cfg.TopicRootPath()
	for _, tag := range tags {
		normalized := normalizeTopicName(tag)
		dirPath := filepath.Join(topicRoot, normalized)
		fi, err := os.Stat(dirPath)
		if err != nil || !fi.IsDir() {
			continue
		}
		indexPath := filepath.Join(dirPath, r.cfg.IndexFilename)
		if _, err := os.Stat(indexPath); err != nil {
			continue
		}
		return indexPath, true
	}
	return "", false
}
