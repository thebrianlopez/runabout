package shellprof

import "fmt"

// FishFunction represents a fish shell function to profile.
type FishFunction struct {
	Name string
	Body string
	File string
}

// InstrumentConfig controls how functions are instrumented.
type InstrumentConfig struct {
	Depth      int
	IncludeEnv bool
}

// Instrument wraps a fish function with timing instrumentation.
func Instrument(fn string, cfg InstrumentConfig) (string, error) {
	return "", fmt.Errorf("not yet implemented")
}
