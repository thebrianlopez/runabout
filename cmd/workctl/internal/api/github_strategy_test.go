package api

import (
	"testing"
	"time"
)

// TestCalculateDateRangeAge tests the date range age calculation
func TestCalculateDateRangeAge(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name      string
		startDate time.Time
		want      int // Approximate expected value
		tolerance int // Allow some tolerance for test execution time
	}{
		{
			name:      "30 days ago",
			startDate: now.AddDate(0, 0, -30),
			want:      30,
			tolerance: 1,
		},
		{
			name:      "90 days ago",
			startDate: now.AddDate(0, 0, -90),
			want:      90,
			tolerance: 1,
		},
		{
			name:      "365 days ago",
			startDate: now.AddDate(0, 0, -365),
			want:      365,
			tolerance: 1,
		},
		{
			name:      "today",
			startDate: now,
			want:      0,
			tolerance: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateDateRangeAge(tt.startDate)

			// Check if result is within tolerance
			diff := got - tt.want
			if diff < 0 {
				diff = -diff
			}
			if diff > tt.tolerance {
				t.Errorf("CalculateDateRangeAge() = %d, want %d (±%d)", got, tt.want, tt.tolerance)
			}
		})
	}
}

// TestCategorizeeDateRange tests the date range categorization
func TestCategorizeeDateRange(t *testing.T) {
	tests := []struct {
		name    string
		daysAgo int
		want    DateRangeCategory
	}{
		{
			name:    "recent: 0 days",
			daysAgo: 0,
			want:    CategoryRecent,
		},
		{
			name:    "recent: 30 days",
			daysAgo: 30,
			want:    CategoryRecent,
		},
		{
			name:    "recent: 90 days (boundary)",
			daysAgo: 90,
			want:    CategoryRecent,
		},
		{
			name:    "historical: 91 days",
			daysAgo: 91,
			want:    CategoryHistorical,
		},
		{
			name:    "historical: 180 days",
			daysAgo: 180,
			want:    CategoryHistorical,
		},
		{
			name:    "historical: 365 days (boundary)",
			daysAgo: 365,
			want:    CategoryHistorical,
		},
		{
			name:    "old: 366 days",
			daysAgo: 366,
			want:    CategoryOld,
		},
		{
			name:    "old: 730 days (2 years)",
			daysAgo: 730,
			want:    CategoryOld,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CategorizeeDateRange(tt.daysAgo)
			if got != tt.want {
				t.Errorf("CategorizeeDateRange(%d) = %v, want %v", tt.daysAgo, got, tt.want)
			}
		})
	}
}

// TestSelectStrategy tests the API strategy selection logic
func TestSelectStrategy(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name      string
		startDate time.Time
		override  APIStrategy
		want      APIStrategy
		wantErr   bool
	}{
		{
			name:      "auto: recent (30 days) -> Events API",
			startDate: now.AddDate(0, 0, -30),
			override:  StrategyAuto,
			want:      StrategyEvents,
			wantErr:   false,
		},
		{
			name:      "auto: recent (90 days) -> Events API",
			startDate: now.AddDate(0, 0, -90),
			override:  StrategyAuto,
			want:      StrategyEvents,
			wantErr:   false,
		},
		{
			name:      "auto: historical (180 days) -> Search API",
			startDate: now.AddDate(0, 0, -180),
			override:  StrategyAuto,
			want:      StrategySearch,
			wantErr:   false,
		},
		{
			name:      "auto: old (400 days) -> GraphQL API",
			startDate: now.AddDate(0, 0, -400),
			override:  StrategyAuto,
			want:      StrategyGraphQL,
			wantErr:   false,
		},
		{
			name:      "manual: force Events API for recent data",
			startDate: now.AddDate(0, 0, -30),
			override:  StrategyEvents,
			want:      StrategyEvents,
			wantErr:   false,
		},
		{
			name:      "manual: force Search API for recent data",
			startDate: now.AddDate(0, 0, -30),
			override:  StrategySearch,
			want:      StrategySearch,
			wantErr:   false,
		},
		{
			name:      "manual: force GraphQL API for recent data",
			startDate: now.AddDate(0, 0, -30),
			override:  StrategyGraphQL,
			want:      StrategyGraphQL,
			wantErr:   false,
		},
		{
			name:      "error: future date",
			startDate: now.AddDate(0, 0, 10),
			override:  StrategyAuto,
			want:      "",
			wantErr:   true,
		},
		{
			name:      "error: invalid override strategy",
			startDate: now.AddDate(0, 0, -30),
			override:  APIStrategy("invalid"),
			want:      "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SelectStrategy(tt.startDate, tt.override)

			if tt.wantErr {
				if err == nil {
					t.Errorf("SelectStrategy() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("SelectStrategy() unexpected error: %v", err)
				return
			}

			if got != tt.want {
				t.Errorf("SelectStrategy() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestValidateStrategyOverride tests the strategy override validation
func TestValidateStrategyOverride(t *testing.T) {
	tests := []struct {
		name     string
		strategy APIStrategy
		wantErr  bool
	}{
		{
			name:     "valid: events",
			strategy: StrategyEvents,
			wantErr:  false,
		},
		{
			name:     "valid: search",
			strategy: StrategySearch,
			wantErr:  false,
		},
		{
			name:     "valid: graphql",
			strategy: StrategyGraphQL,
			wantErr:  false,
		},
		{
			name:     "invalid: auto (not allowed as override)",
			strategy: StrategyAuto,
			wantErr:  true,
		},
		{
			name:     "invalid: empty string",
			strategy: APIStrategy(""),
			wantErr:  true,
		},
		{
			name:     "invalid: unknown strategy",
			strategy: APIStrategy("unknown"),
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateStrategyOverride(tt.strategy)

			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateStrategyOverride() expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("ValidateStrategyOverride() unexpected error: %v", err)
				}
			}
		})
	}
}

// TestGetStrategyDescription tests the strategy description generator
func TestGetStrategyDescription(t *testing.T) {
	tests := []struct {
		name     string
		strategy APIStrategy
		want     string
	}{
		{
			name:     "events API",
			strategy: StrategyEvents,
			want:     "Events API (detailed, last 90 days)",
		},
		{
			name:     "search API",
			strategy: StrategySearch,
			want:     "Search API (PRs/issues, last ~1 year)",
		},
		{
			name:     "graphql API",
			strategy: StrategyGraphQL,
			want:     "GraphQL API (aggregate counts, full history)",
		},
		{
			name:     "auto",
			strategy: StrategyAuto,
			want:     "Auto (selects best API based on date range)",
		},
		{
			name:     "unknown",
			strategy: APIStrategy("unknown"),
			want:     "Unknown strategy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetStrategyDescription(tt.strategy)
			if got != tt.want {
				t.Errorf("GetStrategyDescription() = %q, want %q", got, tt.want)
			}
		})
	}
}

// BenchmarkCalculateDateRangeAge benchmarks the date range age calculation
func BenchmarkCalculateDateRangeAge(b *testing.B) {
	startDate := time.Now().AddDate(0, 0, -180)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		CalculateDateRangeAge(startDate)
	}
}

// BenchmarkSelectStrategy benchmarks the strategy selection
func BenchmarkSelectStrategy(b *testing.B) {
	startDate := time.Now().AddDate(0, 0, -180)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		SelectStrategy(startDate, StrategyAuto)
	}
}
