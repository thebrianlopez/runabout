//go:build !leakcheck

package main

import "testing"

func runTests(m *testing.M) int {
	return m.Run()
}
