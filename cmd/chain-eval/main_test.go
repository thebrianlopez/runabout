package main

import (
	"os"
	"testing"
)

// TestMain redirects the automation-metrics bus to a throwaway directory for
// the whole package.
//
// runIndex emits schema_violation and status_value_observed events, so any test
// that calls it would otherwise append to the operator's real event bus at
// ~/.automation-metrics/events/. That bus is append-only production data used
// for enum tuning and graduation/regression analysis; test fixtures written
// into it are indistinguishable from real observations after the fact.
//
// Guarding it here rather than per-test means a new test cannot reintroduce the
// leak by forgetting to opt out. Individual tests may still override with
// t.Setenv when they need to inspect what was written.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "chain-eval-metrics-*")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("AUTOMATION_METRICS_DIR", dir); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir) //nolint:errcheck
	os.Exit(code)
}
