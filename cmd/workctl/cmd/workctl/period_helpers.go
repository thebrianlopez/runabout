package main

import (
	"fmt"
	"time"
)

// Period represents a named, closed date window [Start, End] (inclusive, YYYY-MM-DD).
type Period struct {
	Label string // human-readable, e.g. "Jan–Mar 2025" or "Oct 2025"
	Start string // "2025-01-01"
	End   string // "2025-03-31"
}

// GeneratePeriods returns n consecutive, non-overlapping periods of equal length
// ending at end, oldest first.  size uses the same format as subtractDuration
// (e.g. "3m", "1m", "7d", "1y").  Returns an error if n < 2 or size is invalid.
func GeneratePeriods(n int, size string, end time.Time) ([]Period, error) {
	if n < 2 {
		return nil, fmt.Errorf("periods must be ≥ 2, got %d", n)
	}
	// Validate size by doing a trial subtraction.
	if _, err := subtractDuration(time.Now(), size); err != nil {
		return nil, fmt.Errorf("invalid period size %q: %w", size, err)
	}

	periods := make([]Period, n)
	curEnd := end

	for i := n - 1; i >= 0; i-- {
		// Use (curEnd+1d) - size so that calendar months align cleanly:
		// e.g. size="3m", curEnd=2025-12-31 → curStart=2025-10-01 (not 2025-09-30).
		curStart, err := subtractDuration(curEnd.AddDate(0, 0, 1), size)
		if err != nil {
			return nil, err
		}
		periods[i] = Period{
			Label: labelPeriod(curStart, curEnd),
			Start: curStart.Format("2006-01-02"),
			End:   curEnd.Format("2006-01-02"),
		}
		// Next iteration ends one day before this period starts.
		curEnd = curStart.AddDate(0, 0, -1)
	}

	return periods, nil
}

// labelPeriod returns a compact human-readable label for the period [start, end].
//   - Full-month window  → "Jan 2025"
//   - Multi-month, same year → "Jan–Mar 2025"
//   - Multi-month, year wrap → "Nov 2025 – Jan 2026"
//   - Partial-month window → "Jan 1–7 2025"
func labelPeriod(start, end time.Time) string {
	sameYear := start.Year() == end.Year()
	sameMonth := sameYear && start.Month() == end.Month()

	if sameMonth {
		// Is it a full calendar month?
		lastDay := time.Date(start.Year(), start.Month()+1, 0, 0, 0, 0, 0, start.Location()).Day()
		if start.Day() == 1 && end.Day() == lastDay {
			return start.Format("Jan 2006")
		}
		return fmt.Sprintf("%s %d–%d %d", start.Format("Jan"), start.Day(), end.Day(), start.Year())
	}
	if sameYear {
		return start.Format("Jan") + "–" + end.Format("Jan 2006")
	}
	return start.Format("Jan 2006") + " – " + end.Format("Jan 2006")
}

// computeWindowFromEnd returns a (start, end) date pair in YYYY-MM-DD format.
// duration is a human string like "7d", "90d", "3m", or "1y".
// If endOverride is non-empty it is parsed as the end date; otherwise today is used.
func computeWindowFromEnd(duration, endOverride string) (string, string, error) {
	var end time.Time
	if endOverride != "" {
		var err error
		end, err = time.Parse("2006-01-02", endOverride)
		if err != nil {
			return "", "", fmt.Errorf("invalid end date %q: %w", endOverride, err)
		}
	} else {
		end = time.Now()
	}

	start, err := subtractDuration(end, duration)
	if err != nil {
		return "", "", err
	}

	return start.Format("2006-01-02"), end.Format("2006-01-02"), nil
}

// subtractDuration subtracts a human-friendly duration from t using calendar-correct
// arithmetic (AddDate). Supported suffixes: d (days), m (months), y (years).
func subtractDuration(t time.Time, duration string) (time.Time, error) {
	if len(duration) < 2 {
		return time.Time{}, fmt.Errorf("duration too short: %q", duration)
	}

	numStr := duration[:len(duration)-1]
	unit := duration[len(duration)-1]

	var n int
	if _, err := fmt.Sscanf(numStr, "%d", &n); err != nil {
		return time.Time{}, fmt.Errorf("invalid number in %q: %w", duration, err)
	}
	if n <= 0 {
		return time.Time{}, fmt.Errorf("duration must be positive: %q", duration)
	}

	switch unit {
	case 'd':
		return t.AddDate(0, 0, -n), nil
	case 'm':
		return t.AddDate(0, -n, 0), nil
	case 'y':
		return t.AddDate(-n, 0, 0), nil
	default:
		return time.Time{}, fmt.Errorf("unknown unit %q in %q (use d/m/y)", string(unit), duration)
	}
}
