package insights

import "io"

// RenderReview writes a combined insights + career report to w.
func RenderReview(w io.Writer, signals *SignalSet, result *TrackResult, period string) {
	RenderInsights(w, signals, period)
	RenderCareer(w, result, period)
}
