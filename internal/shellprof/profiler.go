package shellprof

import "fmt"

// Profile holds the results of profiling a fish function.
type Profile struct {
	Function string
	Records  []CallRecord
	Duration float64
}

// CallRecord captures a single function call during profiling.
type CallRecord struct {
	Function string
	Start    float64
	End      float64
	Duration float64
	Depth    int
	Children []CallRecord
}

// ProfileConfig controls profiling behavior.
type ProfileConfig struct {
	Depth  int
	Format string
}

// Run profiles a fish function and returns the profile data.
func Run(fn string, cfg ProfileConfig) (*Profile, error) {
	return nil, fmt.Errorf("not yet implemented")
}
