package shellprof

import (
	"strings"
	"testing"
)

func TestRunNotImplemented(t *testing.T) {
	_, err := Run("test_func", ProfileConfig{Depth: 1, Format: "text"})
	if err == nil {
		t.Fatal("expected error from unimplemented Run")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("unexpected error: %v", err)
	}
}
