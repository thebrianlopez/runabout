package chainindex

import (
	"io"
	"os"
	"time"
)

// mdqRunner is injectable for tests to avoid real mdq subprocess calls.
var mdqRunner func(args ...string) ([]byte, error) = defaultMdqRunner

func defaultMdqRunner(args ...string) ([]byte, error) {
	cmd := newCmd("mdq", args...)
	return cmd.CombinedOutput()
}

// scannerStderr receives warning messages; overridden in tests.
var scannerStderr io.Writer = os.Stderr

// Scan walks docsRoot and returns ArtifactRecords for all discovered pipeline
// artifacts. A single artifact parse failure emits a warning and does not abort
// the scan. Fatal errors (docs root not found, mdq unavailable) return a non-nil error.
func Scan(docsRoot string, clock func() time.Time) ([]ArtifactRecord, error) {
	panic("not implemented")
}
