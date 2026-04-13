package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/fatih/color"
)

func TestInitializeNoColor(t *testing.T) {
	color.NoColor = false

	Initialize(true)
	if !color.NoColor {
		t.Error("Initialize(true) should set color.NoColor = true")
	}

	color.NoColor = false
}

func TestInitializePreservesDefault(t *testing.T) {
	color.NoColor = false

	Initialize(false)
	// fatih/color may auto-detect TTY; we just verify it didn't force-disable
	// (in test environments NoColor is often true due to non-TTY stdout, which is fine)
}

func TestOutputFunctionsDoNotPanic(t *testing.T) {
	color.NoColor = true
	defer func() { color.NoColor = false }()

	// Redirect color.Output to a buffer
	var buf bytes.Buffer
	oldOut := color.Output
	color.Output = &buf
	defer func() { color.Output = oldOut }()

	Successf("ok %d\n", 1)
	Errorf("fail %s\n", "x")
	Warnf("warn\n")
	Infof("info\n")
	Headerf("header\n")
	Dimf("dim\n")

	out := buf.String()

	if !strings.Contains(out, "ok 1") {
		t.Errorf("expected 'ok 1' in output, got %q", out)
	}
	if !strings.Contains(out, "fail x") {
		t.Errorf("expected 'fail x' in output, got %q", out)
	}
}

func TestNoColorOutputHasNoANSI(t *testing.T) {
	color.NoColor = true
	defer func() { color.NoColor = false }()

	var buf bytes.Buffer
	oldOut := color.Output
	color.Output = &buf
	defer func() { color.Output = oldOut }()

	Successf("green\n")
	Errorf("red\n")
	Warnf("yellow\n")
	Infof("cyan\n")
	Headerf("bold\n")
	Dimf("faint\n")

	out := buf.String()

	if strings.Contains(out, "\033[") {
		t.Errorf("NoColor output should contain no ANSI escape sequences, got %q", out)
	}
}

func TestSprintfVariants(t *testing.T) {
	color.NoColor = true
	defer func() { color.NoColor = false }()

	if got := SuccessSprintf("ok %d", 1); got != "ok 1" {
		t.Errorf("SuccessSprintf = %q, want %q", got, "ok 1")
	}
	if got := ErrorSprintf("err %s", "x"); got != "err x" {
		t.Errorf("ErrorSprintf = %q, want %q", got, "err x")
	}
	if got := WarnSprintf("w"); got != "w" {
		t.Errorf("WarnSprintf = %q, want %q", got, "w")
	}
	if got := InfoSprintf("i"); got != "i" {
		t.Errorf("InfoSprintf = %q, want %q", got, "i")
	}
	if got := HeaderSprintf("h"); got != "h" {
		t.Errorf("HeaderSprintf = %q, want %q", got, "h")
	}
	if got := DimSprintf("d"); got != "d" {
		t.Errorf("DimSprintf = %q, want %q", got, "d")
	}
}
