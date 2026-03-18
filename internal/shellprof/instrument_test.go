package shellprof

import (
	"strings"
	"testing"
)

func TestInstrumentNotImplemented(t *testing.T) {
	_, err := Instrument("test_func", InstrumentConfig{Depth: 1})
	if err == nil {
		t.Fatal("expected error from unimplemented Instrument")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("unexpected error: %v", err)
	}
}
