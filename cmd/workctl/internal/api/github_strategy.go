package api

import (
	"fmt"
	"time"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/config"
)

// APIStrategy defines which GitHub API to use for data collection
type APIStrategy string

const (
	// StrategyAuto automatically selects the best API based on date range
	StrategyAuto APIStrategy = "auto"

	// StrategyEvents uses the Events API (ListEventsPerformedByUser)
	// - Retention: Last 90 days only
	// - Detail level: High (push events, comments, reviews)
	// - Rate limit: 5,000 requests/hour
	// - Best for: Recent activity queries
	StrategyEvents APIStrategy = "events"

	// StrategySearch uses the Search API (SearchIssues)
	// - Retention: ~1 year (undocumented, but observed)
	// - Detail level: Medium (PRs, issues, reviews - no push events)
	// - Rate limit: 30 requests/minute
	// - Best for: Historical queries (90 days - 1 year)
	StrategySearch APIStrategy = "search"

	// StrategyGraphQL uses the GraphQL API (contributionsCollection)
	// - Retention: Full GitHub history
	// - Detail level: Low (aggregate counts only)
	// - Rate limit: 5,000 points/hour
	// - Best for: Multi-year aggregates
	StrategyGraphQL APIStrategy = "graphql"
)

// DateRangeCategory categorizes the age of a date range
type DateRangeCategory int

const (
	// CategoryRecent: <= 90 days ago (Events API optimal)
	CategoryRecent DateRangeCategory = iota
	// CategoryHistorical: 90 days - 1 year ago (Search API optimal)
	CategoryHistorical
	// CategoryOld: > 1 year ago (GraphQL API only option)
	CategoryOld
)

// CalculateDateRangeAge computes the number of days between today and the start date
func CalculateDateRangeAge(startDate time.Time) int {
	now := time.Now()
	duration := now.Sub(startDate)
	return int(duration.Hours() / 24)
}

// CategorizeeDateRange determines which category a date range falls into
func CategorizeeDateRange(daysAgo int) DateRangeCategory {
	switch {
	case daysAgo <= 90:
		return CategoryRecent
	case daysAgo <= 365:
		return CategoryHistorical
	default:
		return CategoryOld
	}
}

// SelectStrategy determines which GitHub API strategy to use based on date range
// and optional manual override.
//
// Parameters:
//   - startDate: The start of the query date range
//   - override: Manual strategy selection (use StrategyAuto for automatic)
//
// Returns:
//   - The selected API strategy
//   - Error if override is invalid or date is in the future
func SelectStrategy(startDate time.Time, override APIStrategy) (APIStrategy, error) {
	// Validate start date is not in the future
	now := time.Now()
	if startDate.After(now) {
		return "", fmt.Errorf("start date %s is in the future", startDate.Format("2006-01-02"))
	}

	// If manual override specified, validate and return it
	if override != StrategyAuto {
		if err := ValidateStrategyOverride(override); err != nil {
			return "", err
		}
		return override, nil
	}

	// Auto-select based on date range age
	daysAgo := CalculateDateRangeAge(startDate)
	category := CategorizeeDateRange(daysAgo)

	switch category {
	case CategoryRecent:
		return StrategyEvents, nil
	case CategoryHistorical:
		return StrategySearch, nil
	case CategoryOld:
		return StrategyGraphQL, nil
	default:
		return StrategyEvents, nil // Fallback to Events API
	}
}

// ValidateStrategyOverride checks if a manual strategy override is valid
func ValidateStrategyOverride(strategy APIStrategy) error {
	switch strategy {
	case StrategyEvents, StrategySearch, StrategyGraphQL:
		return nil
	default:
		return fmt.Errorf("invalid GitHub API strategy: %s (must be: auto, events, search, or graphql)", strategy)
	}
}

// WarnAboutLimitations prints user-friendly warnings about API limitations
// based on the selected strategy and date range.
//
// Parameters:
//   - strategy: The API strategy being used
//   - daysAgo: Number of days from today to start date
//   - quiet: If true, suppress warnings
func WarnAboutLimitations(strategy APIStrategy, daysAgo int, quiet bool) {
	if quiet {
		return
	}

	category := CategorizeeDateRange(daysAgo)

	switch strategy {
	case StrategyEvents:
		// Events API is optimal for recent data
		if category != CategoryRecent {
			// Warning: forcing Events API for old data (will return 0 results)
			fmt.Printf("⚠️  Warning: Events API only retains 90 days of data\n")
			fmt.Printf("   Your query starts %d days ago, which is outside the retention window.\n", daysAgo)
			fmt.Printf("   Consider using --github-api search for historical data.\n\n")
		}

	case StrategySearch:
		// Search API for historical data
		if category == CategoryRecent {
			fmt.Printf("ℹ️  Using Search API for recent data (--github-api search override)\n")
			fmt.Printf("   Note: Events API is faster for data within the last 90 days.\n\n")
		} else {
			fmt.Printf("ℹ️  Date range exceeds 90 days (%d days ago)\n", daysAgo)
			fmt.Printf("   Using GitHub Search API for historical data:\n")
			fmt.Printf("   • Returns: Pull requests, issues, reviews (no push events)\n")
			fmt.Printf("   • Detail level: Medium (PR-level granularity)\n")
			fmt.Printf("   • Rate limit: 30 requests/minute (slower than Events API)\n\n")
		}

	case StrategyGraphQL:
		// GraphQL API for very old data or multi-year queries
		if category == CategoryOld {
			fmt.Printf("ℹ️  Date range exceeds 1 year (%d days ago)\n", daysAgo)
			fmt.Printf("   Using GitHub GraphQL API for aggregate statistics:\n")
			fmt.Printf("   • Returns: Total commits, PRs, reviews (counts only)\n")
			fmt.Printf("   • Detail level: Low (no per-event details or URLs)\n")
			fmt.Printf("   • Private repos: Shown as 'restricted contributions'\n\n")
		} else {
			fmt.Printf("ℹ️  Using GraphQL API (--github-api graphql override)\n")
			fmt.Printf("   Note: GraphQL only provides aggregate counts, not detailed events.\n\n")
		}
	}

	// Additional warning for Events API limitations
	if strategy == StrategyEvents && daysAgo > 90 {
		if config.Debug {
			config.LogDebug("WARNING: Query date range (%d days ago) exceeds Events API 90-day retention", daysAgo)
		}
	}
}

// GetStrategyDescription returns a human-readable description of a strategy
func GetStrategyDescription(strategy APIStrategy) string {
	switch strategy {
	case StrategyEvents:
		return "Events API (detailed, last 90 days)"
	case StrategySearch:
		return "Search API (PRs/issues, last ~1 year)"
	case StrategyGraphQL:
		return "GraphQL API (aggregate counts, full history)"
	case StrategyAuto:
		return "Auto (selects best API based on date range)"
	default:
		return "Unknown strategy"
	}
}
