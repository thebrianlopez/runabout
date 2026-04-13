package ai

import (
	"fmt"
)

// Process is the single entry point for the data processing pipeline.
// It reads JSON exports, correlates users across sources, builds a unified
// timeline, and computes aggregate metrics.
func Process(opts ProcessOptions) (*ProcessedData, error) {
	// 1. Parse available exports (skip missing files, error on parse failures)
	var exports []*ParsedExport

	type sourceParser struct {
		path  string
		parse func(string) (*ParsedExport, error)
		label string
	}

	parsers := []sourceParser{
		{opts.JiraPath, parseJiraExport, "jira"},
		{opts.ConfluencePath, parseConfluenceExport, "confluence"},
		{opts.GitHubPath, parseGitHubExport, "github"},
	}

	for _, sp := range parsers {
		if sp.path == "" || !fileExists(sp.path) {
			continue
		}
		exp, err := sp.parse(sp.path)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s export: %w", sp.label, err)
		}
		exports = append(exports, exp)
	}

	// 2. Require at least one valid source
	if len(exports) == 0 {
		return nil, fmt.Errorf("no valid data sources found")
	}

	// 3. Resolve user identity
	identity := resolveIdentity(opts, exports)

	// 4. Extract date range from first available metadata
	dateRange, err := extractDateRange(exports[0].Metadata)
	if err != nil {
		return nil, fmt.Errorf("extracting date range: %w", err)
	}

	// 5. Build unified timeline
	timeline := buildTimeline(exports)

	// 6. Compute aggregate metrics
	metrics := computeMetrics(exports, dateRange)

	// 7. Return assembled result
	return &ProcessedData{
		Identity: identity,
		Timeline: timeline,
		Metrics:  metrics,
	}, nil
}
