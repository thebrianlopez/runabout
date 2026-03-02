package perfgate

import (
	"math"
	"testing"
)

func TestComputeEmpty(t *testing.T) {
	s := Compute(nil)
	if s.N != 0 {
		t.Errorf("expected N=0, got %d", s.N)
	}
	if s.Mean != 0 {
		t.Errorf("expected Mean=0, got %f", s.Mean)
	}
}

func TestComputeSingleSample(t *testing.T) {
	s := Compute([]float64{0.5})
	if s.N != 1 {
		t.Errorf("expected N=1, got %d", s.N)
	}
	if s.Mean != 0.5 {
		t.Errorf("expected Mean=0.5, got %f", s.Mean)
	}
	if s.Median != 0.5 {
		t.Errorf("expected Median=0.5, got %f", s.Median)
	}
	if s.Min != 0.5 || s.Max != 0.5 {
		t.Errorf("expected Min=Max=0.5, got Min=%f Max=%f", s.Min, s.Max)
	}
	if s.StdDev != 0 {
		t.Errorf("expected StdDev=0, got %f", s.StdDev)
	}
}

func TestComputeOddSamples(t *testing.T) {
	// 1, 2, 3, 4, 5 → mean=3, median=3, min=1, max=5
	s := Compute([]float64{3, 1, 5, 2, 4})
	if s.N != 5 {
		t.Errorf("expected N=5, got %d", s.N)
	}
	if s.Mean != 3.0 {
		t.Errorf("expected Mean=3.0, got %f", s.Mean)
	}
	if s.Median != 3.0 {
		t.Errorf("expected Median=3.0, got %f", s.Median)
	}
	if s.Min != 1.0 {
		t.Errorf("expected Min=1.0, got %f", s.Min)
	}
	if s.Max != 5.0 {
		t.Errorf("expected Max=5.0, got %f", s.Max)
	}
}

func TestComputeEvenSamples(t *testing.T) {
	// 1, 2, 3, 4 → median = (2+3)/2 = 2.5
	s := Compute([]float64{4, 2, 1, 3})
	if s.Median != 2.5 {
		t.Errorf("expected Median=2.5, got %f", s.Median)
	}
}

func TestComputeStdDev(t *testing.T) {
	// [2, 4, 4, 4, 5, 5, 7, 9] → mean=5, stddev=2
	s := Compute([]float64{2, 4, 4, 4, 5, 5, 7, 9})
	if s.Mean != 5.0 {
		t.Errorf("expected Mean=5.0, got %f", s.Mean)
	}
	if math.Abs(s.StdDev-2.0) > 0.001 {
		t.Errorf("expected StdDev≈2.0, got %f", s.StdDev)
	}
}

func TestComputeP95(t *testing.T) {
	// 20 samples: 1..20, P95 = element at ceil(0.95*20)-1 = 19-1 = 18 → value 19
	samples := make([]float64, 20)
	for i := range samples {
		samples[i] = float64(i + 1)
	}
	s := Compute(samples)
	if s.P95 != 19.0 {
		t.Errorf("expected P95=19.0, got %f", s.P95)
	}
}

func TestComputeDoesNotMutateInput(t *testing.T) {
	input := []float64{5, 3, 1, 4, 2}
	original := make([]float64, len(input))
	copy(original, input)

	Compute(input)

	for i := range input {
		if input[i] != original[i] {
			t.Errorf("input was mutated at index %d: got %f, want %f", i, input[i], original[i])
		}
	}
}
