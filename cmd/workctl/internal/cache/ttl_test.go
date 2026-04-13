package cache

import (
	"testing"
	"time"
)

func TestDefaultTTLs(t *testing.T) {
	tests := []struct {
		source string
		want   time.Duration
	}{
		{SourceJira, 1 * time.Hour},
		{SourceConfluence, 1 * time.Hour},
		{SourceGitHubEvents, 15 * time.Minute},
		{SourceGitHubSearch, 24 * time.Hour},
		{SourceGitHubGraphQL, 24 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			if got := DefaultTTLs[tt.source]; got != tt.want {
				t.Errorf("DefaultTTLs[%s] = %v, want %v", tt.source, got, tt.want)
			}
		})
	}
}

func TestTTLFor(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		got := TTLFor(SourceJira, nil)
		if got != 1*time.Hour {
			t.Errorf("TTLFor(%s, nil) = %v, want 1h", SourceJira, got)
		}
	})

	t.Run("override", func(t *testing.T) {
		overrides := map[string]time.Duration{
			SourceJira: 30 * time.Minute,
		}
		got := TTLFor(SourceJira, overrides)
		if got != 30*time.Minute {
			t.Errorf("TTLFor(%s, overrides) = %v, want 30m", SourceJira, got)
		}
	})

	t.Run("unknown source fallback", func(t *testing.T) {
		got := TTLFor("unknown_source", nil)
		if got != 1*time.Hour {
			t.Errorf("TTLFor(unknown, nil) = %v, want 1h", got)
		}
	})

	t.Run("override for unknown source", func(t *testing.T) {
		overrides := map[string]time.Duration{
			"custom": 5 * time.Minute,
		}
		got := TTLFor("custom", overrides)
		if got != 5*time.Minute {
			t.Errorf("TTLFor(custom, overrides) = %v, want 5m", got)
		}
	})
}
