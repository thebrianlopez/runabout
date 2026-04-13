package insights

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderReview(t *testing.T) {
	var buf bytes.Buffer
	signals := &SignalSet{ThemeCounts: make(map[Theme]int)}
	result := &TrackResult{
		Track:       "staff",
		Description: "Staff engineer",
		Overall:     0.75,
		Dimensions: []DimensionScore{
			{Name: "cross_team_impact", Raw: 0.5, Normalized: 0.5, Weight: 0.25, Weighted: 0.125},
		},
	}

	RenderReview(&buf, signals, result, "2025-01-01 to 2025-12-31")

	out := buf.String()
	if !strings.Contains(out, "# Career Growth Insights") {
		t.Error("missing insights header")
	}
	if !strings.Contains(out, "# Career Track Analysis:") {
		t.Error("missing career header")
	}
}
