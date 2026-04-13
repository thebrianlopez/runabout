package main

import (
	"testing"
)

func TestParseDurationFlexible_Days(t *testing.T) {
	d, err := parseDurationFlexible("7d")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != 7*24*60*60*1e9 {
		t.Errorf("7d = %v, want 7 days", d)
	}
}

func TestParseDurationFlexible_Hours(t *testing.T) {
	d, err := parseDurationFlexible("24h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != 24*60*60*1e9 {
		t.Errorf("24h = %v, want 24 hours", d)
	}
}

func TestParseDurationFlexible_InvalidDay(t *testing.T) {
	_, err := parseDurationFlexible("0d")
	if err == nil {
		t.Error("expected error for 0d")
	}
}

func TestParseDurationFlexible_Invalid(t *testing.T) {
	_, err := parseDurationFlexible("abc")
	if err == nil {
		t.Error("expected error for 'abc'")
	}
}

func TestHumanSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1500, "1.5 KB"},
		{1048576, "1.0 MB"},
		{2621440, "2.5 MB"},
	}
	for _, tt := range tests {
		got := humanSize(tt.bytes)
		if got != tt.want {
			t.Errorf("humanSize(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

func TestCacheWarmCmd_Flags(t *testing.T) {
	cmd := cacheWarmCmd()

	// Verify expected flags exist
	for _, flag := range []string{"periods", "period-size", "end"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("missing flag: %s", flag)
		}
	}

	// Verify default values
	n, _ := cmd.Flags().GetInt("periods")
	if n != 0 {
		t.Errorf("default periods = %d, want 0", n)
	}
	ps, _ := cmd.Flags().GetString("period-size")
	if ps != "3m" {
		t.Errorf("default period-size = %q, want %q", ps, "3m")
	}
}
