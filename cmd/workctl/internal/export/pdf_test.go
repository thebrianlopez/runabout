package export_test

import (
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/export"
)

func TestConvertToPDF_PandocAbsent(t *testing.T) {
	// Only run this test when pandoc is actually absent — otherwise it would
	// attempt a real conversion and require a PDF engine.
	if _, err := exec.LookPath("pandoc"); err == nil {
		t.Skip("pandoc is present on this machine; skipping absence test")
	}

	err := export.ConvertToPDF("/tmp/workctl_test_absent.pdf", func(w io.Writer) error {
		_, err := w.Write([]byte("# Test"))
		return err
	})

	if err == nil {
		t.Fatal("expected error when pandoc absent, got nil")
	}
	if !errors.Is(err, export.ErrPandocNotFound) {
		t.Errorf("want ErrPandocNotFound, got: %v", err)
	}
	if !strings.Contains(err.Error(), "brew install pandoc") {
		t.Errorf("error should contain install hint, got: %v", err)
	}
}

func TestPandocAvailable(t *testing.T) {
	_, lookErr := exec.LookPath("pandoc")
	got := export.PandocAvailable()
	want := lookErr == nil
	if got != want {
		t.Errorf("PandocAvailable(): want %v, got %v", want, got)
	}
}
