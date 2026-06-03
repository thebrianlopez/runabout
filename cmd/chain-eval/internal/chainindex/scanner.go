package chainindex

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// scannerStderr receives warning messages; overridden in tests.
var scannerStderr io.Writer = os.Stderr

// timestampPrefixRe matches the org+timestamp prefix in artifact filenames:
// e.g. "PERSONAL_20260101T000000Z_" or "POMO_" variants.
var timestampPrefixRe = regexp.MustCompile(`^[A-Z]+_\d{8}T\d{6}Z_`)

// Scan walks docsRoot and returns ArtifactRecords for all discovered pipeline
// artifacts. A single artifact parse failure emits a warning and does not abort
// the scan. Fatal errors (docs root not found) return a non-nil error.
func Scan(docsRoot string, _ func() time.Time) ([]ArtifactRecord, error) {
	if _, err := os.Stat(docsRoot); os.IsNotExist(err) {
		return nil, fmt.Errorf("docs root not found at %s", docsRoot)
	}

	var records []ArtifactRecord

	type dirSpec struct {
		dir     string
		typeFn  func(name string) (ArtifactType, bool)
	}
	dirs := []dirSpec{
		{"prds", func(n string) (ArtifactType, bool) { return ArtifactPRD, strings.HasSuffix(n, ".md") }},
		{"design", detectDesignType},
		{"epics", func(n string) (ArtifactType, bool) { return ArtifactEpic, strings.HasSuffix(n, ".md") }},
		{"releases", func(n string) (ArtifactType, bool) { return ArtifactRelease, strings.HasSuffix(n, ".md") }},
		{"pomo", func(n string) (ArtifactType, bool) { return ArtifactPOMO, strings.HasSuffix(n, ".md") }},
		{"context", func(n string) (ArtifactType, bool) { return ArtifactSidecar, strings.HasSuffix(n, ".md") }},
	}

	for _, spec := range dirs {
		dirPath := filepath.Join(docsRoot, spec.dir)
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			continue // directory absent - skip silently
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			artifactType, ok := spec.typeFn(e.Name())
			if !ok {
				continue
			}
			fullPath := filepath.Join(dirPath, e.Name())
			content, err := os.ReadFile(fullPath)
			if err != nil {
				rel, _ := filepath.Rel(docsRoot, fullPath)
				fmt.Fprintf(scannerStderr, "chain-eval index: artifact_parse_error %s: %v - skipped\n", rel, err)
				continue
			}
			if len(strings.TrimSpace(string(content))) == 0 {
				rel, _ := filepath.Rel(docsRoot, fullPath)
				fmt.Fprintf(scannerStderr, "chain-eval index: parse error %s: empty artifact - skipped\n", rel)
				continue
			}
			rel, _ := filepath.Rel(docsRoot, fullPath)
			record := parseArtifact(rel, artifactType, string(content))
			records = append(records, record)
		}
	}

	return records, nil
}

func detectDesignType(name string) (ArtifactType, bool) {
	switch {
	case strings.HasSuffix(name, "_FDD.md"):
		return ArtifactFDD, true
	case strings.HasSuffix(name, "_TDD.md"):
		return ArtifactTDD, true
	default:
		return "", false
	}
}

func parseArtifact(rel string, typ ArtifactType, content string) ArtifactRecord {
	result := ExtractStatus(content)

	record := ArtifactRecord{
		Path:               rel,
		Type:               typ,
		Status:             result.Canonical,
		StatusSurfaceDrift: result.SurfaceDrift,
		CreatedAt:          extractCreatedAt(content, rel),
		FeatureID:          extractTableField(content, "Feature ID"),
		UpstreamField:      extractUpstreamField(content),
		IsProtocol:         extractIsProtocol(content),
	}
	if result.SurfaceDrift {
		s := result.Surfaces
		record.StatusSurfaces = &s
	}
	return record
}

// extractCreatedAt returns the created timestamp from the markdown table or
// from the filename timestamp prefix.
func extractCreatedAt(content, rel string) string {
	if v := extractTableField(content, "Created"); v != "" {
		return strings.Trim(v, "`")
	}
	if v := extractTableField(content, "Created At (UTC)"); v != "" {
		return strings.Trim(v, "`")
	}
	// Fall back to filename timestamp.
	base := filepath.Base(rel)
	if m := regexp.MustCompile(`^[A-Z]+_(\d{8}T\d{6}Z)_`).FindStringSubmatch(base); len(m) > 1 {
		return m[1]
	}
	return ""
}

// extractTableField parses `| **Field** | value |` from markdown table rows.
func extractTableField(content, field string) string {
	pattern := regexp.MustCompile(`(?m)^\|\s*\*\*` + regexp.QuoteMeta(field) + `\*\*\s*\|\s*` + "`?" + `([^|` + "`" + `]+?)` + "`?" + `\s*\|`)
	if m := pattern.FindStringSubmatch(content); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// extractUpstreamField returns the most specific upstream reference:
// Source TDD > Source FDD > Source PRD.
func extractUpstreamField(content string) string {
	if v := extractTableField(content, "Source TDD"); v != "" {
		return v
	}
	if v := extractTableField(content, "Source FDD"); v != "" {
		return v
	}
	if v := extractTableField(content, "Source PRD"); v != "" {
		return v
	}
	return ""
}

// extractIsProtocol returns true when the artifact declares `Protocol Spec: true`.
func extractIsProtocol(content string) bool {
	v := extractTableField(content, "Protocol Spec")
	return strings.EqualFold(strings.TrimSpace(v), "true")
}
