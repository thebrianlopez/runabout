package main

// EPIC-155 F2: Intent-Conditioned Prompt Construction.
// Replaces profile-based rubric loading with intent+tag-keyed YAML files.
// Files live in docs/prompts/intents/{intent}.yaml and overrides/{tag}.yaml.
// Cache is invalidated by SIGHUP (same reload mechanism as profile YAML).

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// intentRubricFile is the on-disk schema for docs/prompts/intents/{intent}.yaml.
type intentRubricFile struct {
	Intent       string            `yaml:"intent"`
	ContentTypes map[string]string `yaml:"content_types"`
	Default      string            `yaml:"default"`
}

// tagOverrideFile is the on-disk schema for docs/prompts/intents/overrides/{tag}.yaml.
type tagOverrideFile struct {
	Tag          string `yaml:"tag"`
	Instructions string `yaml:"instructions"`
}

// intentRubricCache caches parsed rubric files to avoid per-call disk reads.
// Invalidated by intentRubricCacheMu write lock (e.g., on SIGHUP).
var (
	intentRubricCache    map[string]*intentRubricFile
	tagOverrideCache     map[string]*tagOverrideFile
	intentRubricCacheMu  sync.RWMutex
)

// IntentSearchPath returns the directories to search for intent YAML files.
// Priority: LINKARI_INTENT_PATH env → ORG_PATH → home → testdata/intents
func IntentSearchPath() []string {
	var paths []string
	if envPath := os.Getenv("LINKARI_INTENT_PATH"); envPath != "" {
		paths = append(paths, envPath)
	}
	if orgPath := os.Getenv("ORG_PATH"); orgPath != "" {
		paths = append(paths, filepath.Join(orgPath, "docs", "prompts", "intents"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, "code", "personal", "docs", "prompts", "intents"))
	}
	paths = append(paths, "testdata/intents")
	return paths
}

// loadIntentRubric constructs the system prompt for intent-conditioned scoring.
// userTags: from queue.user_tags (explicit user intent - listed first, labeled "User-Applied")
// inferredTags: from queue.inferred_tags (system-inferred domain context - labeled "Inferred")
// Returns non-empty string; falls back to a minimal preamble on rubric parse failure (RG-1).
func loadIntentRubric(intent, contentType string, userTags, inferredTags []string) (string, error) {
	rubric, err := loadIntentRubricFile(intent)
	if err != nil {
		slog.Error("intent_rubric_not_found",
			"error_class", "intent_rubric_not_found",
			"intent", intent,
			"error", err,
		)
		// RG-1: fall back to minimal preamble - never return empty.
		return intentMinimalPreamble(intent, userTags), nil
	}

	// Select content-type-specific section or default.
	var base string
	if rubric.ContentTypes != nil {
		if section, ok := rubric.ContentTypes[contentType]; ok && strings.TrimSpace(section) != "" {
			base = section
		}
	}
	if base == "" {
		base = rubric.Default
	}
	if strings.TrimSpace(base) == "" {
		slog.Warn("intent_rubric_empty",
			"intent", intent,
			"content_type", contentType,
		)
		return intentMinimalPreamble(intent, userTags), nil
	}

	// Build preamble: intent + tags context.
	var sb strings.Builder
	sb.WriteString(classificationPreambleIntent(intent, userTags, inferredTags, contentType))
	sb.WriteString(base)
	sb.WriteString("\n")

	// Apply tag overrides for capture-relevant tags.
	appliedOverrides := applyTagOverrides(&sb, intent, userTags)
	if len(appliedOverrides) > 0 {
		slog.Info("rubric_loaded",
			"intent", intent,
			"content_type", contentType,
			"override_tags", appliedOverrides,
		)
	} else {
		slog.Info("rubric_loaded",
			"intent", intent,
			"content_type", contentType,
		)
	}

	return sb.String(), nil
}

// classificationPreambleIntent returns the framing context prefix for intent-conditioned scoring.
// userTags appear before inferredTags (BT-1) and are labeled distinctly (CT-5).
func classificationPreambleIntent(intent string, userTags, inferredTags []string, contentType string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("User intent: %s", intent))
	if contentType != "" && contentType != "url" {
		sb.WriteString(fmt.Sprintf(" (content type: %s)", contentType))
	}
	sb.WriteString(".\n")

	// CT-6: stable format.
	if len(userTags) > 0 {
		sb.WriteString(fmt.Sprintf("User-Applied Tags: %s\n", strings.Join(userTags, ", ")))
		sb.WriteString("Note: These tags represent explicit user intent and should be weighted accordingly.\n")
	}
	if len(inferredTags) > 0 {
		sb.WriteString(fmt.Sprintf("Inferred Context: %s\n", strings.Join(inferredTags, ", ")))
	}
	sb.WriteString("\n")
	return sb.String()
}

// applyTagOverrides appends per-tag override instructions for capture-relevant tags.
// Returns the list of tags for which overrides were applied.
func applyTagOverrides(sb *strings.Builder, intent string, userTags []string) []string {
	var applied []string
	for _, tag := range userTags {
		if !captureRelevantTags[tag] {
			continue
		}
		override, err := loadTagOverrideFile(tag)
		if err != nil {
			slog.Info("tag_override_not_found",
				"tag", tag,
				"intent", intent,
			)
			continue
		}
		if strings.TrimSpace(override.Instructions) == "" {
			continue
		}
		sb.WriteString("\n")
		sb.WriteString(override.Instructions)
		sb.WriteString("\n")
		slog.Info("tag_override_applied",
			"tag", tag,
			"intent", intent,
		)
		applied = append(applied, tag)
	}
	return applied
}

// loadIntentRubricFile loads and caches the YAML rubric for the given intent.
func loadIntentRubricFile(intent string) (*intentRubricFile, error) {
	intentRubricCacheMu.RLock()
	if intentRubricCache != nil {
		if r, ok := intentRubricCache[intent]; ok {
			intentRubricCacheMu.RUnlock()
			return r, nil
		}
	}
	intentRubricCacheMu.RUnlock()

	// Cache miss - read from disk.
	var rf intentRubricFile
	var lastErr error
	for _, dir := range IntentSearchPath() {
		path := filepath.Join(dir, intent+".yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			lastErr = err
			continue
		}
		if err := yaml.Unmarshal(data, &rf); err != nil {
			slog.Error("rubric_parse_failed",
				"error_class", "rubric_parse_failed",
				"intent", intent,
				"error", err,
			)
			lastErr = fmt.Errorf("rubric_parse_failed: %w", err)
			continue
		}

		intentRubricCacheMu.Lock()
		if intentRubricCache == nil {
			intentRubricCache = make(map[string]*intentRubricFile)
		}
		intentRubricCache[intent] = &rf
		intentRubricCacheMu.Unlock()
		return &rf, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("intent rubric %q not found in search path", intent)
}

// loadTagOverrideFile loads and caches the YAML override for the given tag.
func loadTagOverrideFile(tag string) (*tagOverrideFile, error) {
	intentRubricCacheMu.RLock()
	if tagOverrideCache != nil {
		if r, ok := tagOverrideCache[tag]; ok {
			intentRubricCacheMu.RUnlock()
			return r, nil
		}
	}
	intentRubricCacheMu.RUnlock()

	var tf tagOverrideFile
	for _, dir := range IntentSearchPath() {
		path := filepath.Join(dir, "overrides", tag+".yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if err := yaml.Unmarshal(data, &tf); err != nil {
			continue
		}

		intentRubricCacheMu.Lock()
		if tagOverrideCache == nil {
			tagOverrideCache = make(map[string]*tagOverrideFile)
		}
		tagOverrideCache[tag] = &tf
		intentRubricCacheMu.Unlock()
		return &tf, nil
	}
	return nil, fmt.Errorf("tag override %q not found", tag)
}

// invalidateIntentRubricCache clears the in-memory rubric cache.
// Called on SIGHUP to force reload from disk on the next request.
func invalidateIntentRubricCache() {
	intentRubricCacheMu.Lock()
	intentRubricCache = nil
	tagOverrideCache = nil
	intentRubricCacheMu.Unlock()
	slog.Info("intent_rubric_cache_invalidated")
}

// intentMinimalPreamble is the fallback prompt used when the intent YAML file
// is missing or unparseable. Always non-empty (RG-1).
func intentMinimalPreamble(intent string, userTags []string) string {
	s := fmt.Sprintf("Evaluate this content for user with intent: %s.\n", intent)
	if len(userTags) > 0 {
		s += fmt.Sprintf("User tags: %s.\n", strings.Join(userTags, ", "))
	}
	return s
}
