package main

import (
	"strings"
	"testing"
	"time"
)

func TestComputeWindowFromEnd_NoOverride(t *testing.T) {
	start, end, err := computeWindowFromEnd("7d", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	today := time.Now().Format("2006-01-02")
	if end != today {
		t.Errorf("end = %q, want %q", end, today)
	}
	expected := time.Now().AddDate(0, 0, -7).Format("2006-01-02")
	if start != expected {
		t.Errorf("start = %q, want %q", start, expected)
	}
}

func TestComputeWindowFromEnd_WithOverride(t *testing.T) {
	start, end, err := computeWindowFromEnd("7d", "2025-06-15")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if end != "2025-06-15" {
		t.Errorf("end = %q, want %q", end, "2025-06-15")
	}
	if start != "2025-06-08" {
		t.Errorf("start = %q, want %q", start, "2025-06-08")
	}
}

func TestComputeWindowFromEnd_InvalidEndDate(t *testing.T) {
	_, _, err := computeWindowFromEnd("7d", "not-a-date")
	if err == nil {
		t.Fatal("expected error for invalid end date")
	}
}

func TestSubtractDuration_Days(t *testing.T) {
	base := time.Date(2025, 3, 15, 0, 0, 0, 0, time.UTC)
	got, err := subtractDuration(base, "7d")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2025, 3, 8, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSubtractDuration_90Days(t *testing.T) {
	base := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	got, err := subtractDuration(base, "90d")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2025, 3, 3, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSubtractDuration_Months(t *testing.T) {
	base := time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC)
	got, err := subtractDuration(base, "3m")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2025, 3, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSubtractDuration_Years(t *testing.T) {
	base := time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC)
	got, err := subtractDuration(base, "1y")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSubtractDuration_InvalidUnit(t *testing.T) {
	base := time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC)
	_, err := subtractDuration(base, "7x")
	if err == nil {
		t.Fatal("expected error for invalid unit")
	}
}

func TestSubtractDuration_InvalidNumber(t *testing.T) {
	base := time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC)
	_, err := subtractDuration(base, "abcd")
	if err == nil {
		t.Fatal("expected error for invalid number")
	}
}

// --------------------------------------------------------------------------
// GeneratePeriods
// --------------------------------------------------------------------------

func TestGeneratePeriods_FourQuarterly(t *testing.T) {
	end := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	periods, err := GeneratePeriods(4, "3m", end)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(periods) != 4 {
		t.Fatalf("got %d periods, want 4", len(periods))
	}

	// Oldest first — four calendar quarters of 2025
	cases := []struct{ start, end string }{
		{"2025-01-01", "2025-03-31"},
		{"2025-04-01", "2025-06-30"},
		{"2025-07-01", "2025-09-30"},
		{"2025-10-01", "2025-12-31"},
	}
	for i, c := range cases {
		if periods[i].Start != c.start {
			t.Errorf("periods[%d].Start = %q, want %q", i, periods[i].Start, c.start)
		}
		if periods[i].End != c.end {
			t.Errorf("periods[%d].End = %q, want %q", i, periods[i].End, c.end)
		}
	}
}

func TestGeneratePeriods_NonOverlapping(t *testing.T) {
	end := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	periods, err := GeneratePeriods(4, "3m", end)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := 1; i < len(periods); i++ {
		prevEnd, _ := time.Parse("2006-01-02", periods[i-1].End)
		currStart, _ := time.Parse("2006-01-02", periods[i].Start)
		if !prevEnd.AddDate(0, 0, 1).Equal(currStart) {
			t.Errorf("gap/overlap between period %d (%s) and %d (%s)",
				i-1, periods[i-1].End, i, periods[i].Start)
		}
	}
}

func TestGeneratePeriods_TwoMonths(t *testing.T) {
	end := time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC)
	periods, err := GeneratePeriods(2, "1m", end)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if periods[0].Start != "2025-02-01" || periods[0].End != "2025-02-28" {
		t.Errorf("period[0] = [%s, %s], want [2025-02-01, 2025-02-28]", periods[0].Start, periods[0].End)
	}
	if periods[1].Start != "2025-03-01" || periods[1].End != "2025-03-31" {
		t.Errorf("period[1] = [%s, %s], want [2025-03-01, 2025-03-31]", periods[1].Start, periods[1].End)
	}
}

func TestGeneratePeriods_YearWrap(t *testing.T) {
	// 2 quarters crossing a year boundary: end = 2026-01-31
	end := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	periods, err := GeneratePeriods(2, "3m", end)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if periods[1].Start != "2025-11-01" || periods[1].End != "2026-01-31" {
		t.Errorf("newest period = [%s, %s], want [2025-11-01, 2026-01-31]", periods[1].Start, periods[1].End)
	}
	if periods[0].Start != "2025-08-01" || periods[0].End != "2025-10-31" {
		t.Errorf("oldest period = [%s, %s], want [2025-08-01, 2025-10-31]", periods[0].Start, periods[0].End)
	}
}

func TestGeneratePeriods_WeeklyWithinMonth(t *testing.T) {
	end := time.Date(2025, 1, 14, 0, 0, 0, 0, time.UTC)
	periods, err := GeneratePeriods(2, "7d", end)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if periods[0].Start != "2025-01-01" || periods[0].End != "2025-01-07" {
		t.Errorf("period[0] = [%s, %s], want [2025-01-01, 2025-01-07]", periods[0].Start, periods[0].End)
	}
	if periods[1].Start != "2025-01-08" || periods[1].End != "2025-01-14" {
		t.Errorf("period[1] = [%s, %s], want [2025-01-08, 2025-01-14]", periods[1].Start, periods[1].End)
	}
}

func TestGeneratePeriods_TooFewPeriods(t *testing.T) {
	end := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	_, err := GeneratePeriods(1, "3m", end)
	if err == nil {
		t.Fatal("expected error for n < 2")
	}
	if !strings.Contains(err.Error(), "≥ 2") {
		t.Errorf("error = %q, want to contain '≥ 2'", err.Error())
	}
}

func TestGeneratePeriods_InvalidSize(t *testing.T) {
	end := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	_, err := GeneratePeriods(4, "xyz", end)
	if err == nil {
		t.Fatal("expected error for invalid size")
	}
}

func TestGeneratePeriods_QuarterlyLabels(t *testing.T) {
	end := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	periods, err := GeneratePeriods(4, "3m", end)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"Jan–Mar 2025", "Apr–Jun 2025", "Jul–Sep 2025", "Oct–Dec 2025"}
	for i, p := range periods {
		if p.Label != want[i] {
			t.Errorf("periods[%d].Label = %q, want %q", i, p.Label, want[i])
		}
	}
}

func TestGeneratePeriods_MonthlyLabels(t *testing.T) {
	end := time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC)
	periods, err := GeneratePeriods(3, "1m", end)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"Jan 2025", "Feb 2025", "Mar 2025"}
	for i, p := range periods {
		if p.Label != want[i] {
			t.Errorf("periods[%d].Label = %q, want %q", i, p.Label, want[i])
		}
	}
}

func TestGeneratePeriods_YearWrapLabel(t *testing.T) {
	end := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	periods, err := GeneratePeriods(2, "3m", end)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// newest period spans year boundary
	if periods[1].Label != "Nov 2025 – Jan 2026" {
		t.Errorf("newest label = %q, want \"Nov 2025 – Jan 2026\"", periods[1].Label)
	}
}
