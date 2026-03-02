package perfgate

import (
	"math"
	"sort"
)

// Stats holds computed statistics for a set of samples.
type Stats struct {
	Mean   float64
	Median float64
	P95    float64
	StdDev float64
	Min    float64
	Max    float64
	N      int
}

// Compute calculates statistics from a slice of duration samples.
func Compute(samples []float64) Stats {
	n := len(samples)
	if n == 0 {
		return Stats{}
	}

	sorted := make([]float64, n)
	copy(sorted, samples)
	sort.Float64s(sorted)

	var sum float64
	for _, v := range sorted {
		sum += v
	}
	mean := sum / float64(n)

	var median float64
	if n%2 == 0 {
		median = (sorted[n/2-1] + sorted[n/2]) / 2
	} else {
		median = sorted[n/2]
	}

	p95Idx := int(math.Ceil(0.95*float64(n))) - 1
	if p95Idx < 0 {
		p95Idx = 0
	}

	var variance float64
	for _, v := range sorted {
		d := v - mean
		variance += d * d
	}
	stddev := math.Sqrt(variance / float64(n))

	return Stats{
		Mean:   mean,
		Median: median,
		P95:    sorted[p95Idx],
		StdDev: stddev,
		Min:    sorted[0],
		Max:    sorted[n-1],
		N:      n,
	}
}
