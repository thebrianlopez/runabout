package export

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// ErrPandocNotFound is returned when pandoc is not on PATH.
var ErrPandocNotFound = errors.New(
	"pandoc not found on PATH — PDF export requires pandoc\n" +
		"  Install: brew install pandoc\n" +
		"  Note: PDF generation also requires a PDF engine such as:\n" +
		"    brew install basictex    # LaTeX-based (recommended)\n" +
		"    brew install wkhtmltopdf # HTML-based alternative",
)

// ConvertToPDF writes the Markdown produced by mdWriter to a temp file,
// then calls pandoc to convert it to a PDF at pdfPath.
//
// Returns ErrPandocNotFound if pandoc is not installed.
// Returns a descriptive error if pandoc is found but conversion fails
// (e.g. missing PDF engine).
func ConvertToPDF(pdfPath string, mdWriter func(w io.Writer) error) error {
	if _, err := exec.LookPath("pandoc"); err != nil {
		return ErrPandocNotFound
	}

	// Write markdown to a temp file in the same directory as the output PDF.
	tmp, err := os.CreateTemp("", "workctl-*.md")
	if err != nil {
		return fmt.Errorf("creating temp markdown file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := mdWriter(tmp); err != nil {
		tmp.Close()
		return fmt.Errorf("rendering markdown for PDF: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("flushing temp markdown file: %w", err)
	}

	// Run pandoc. pandoc infers output format from the .pdf extension.
	// Users may need a PDF engine installed (basictex, wkhtmltopdf, etc.).
	//nolint:gosec // pdfPath is caller-controlled, not user-supplied shell input
	cmd := exec.Command("pandoc", tmpPath, "-o", pdfPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pandoc conversion failed: %w\n%s\n"+
			"Tip: install a PDF engine — e.g. `brew install basictex`",
			err, string(out))
	}

	return nil
}

// PandocAvailable returns true if pandoc is found on PATH.
func PandocAvailable() bool {
	_, err := exec.LookPath("pandoc")
	return err == nil
}
