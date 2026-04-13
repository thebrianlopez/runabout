package ui

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/fatih/color"
)

// Spinner provides a simple \r-based progress indicator for long-running operations.
// It overwrites the current terminal line to show status updates.
// When stdout is not a TTY (piped, --no-color, CI), it falls back to plain log lines.
type Spinner struct {
	w       io.Writer
	mu      sync.Mutex
	active  bool
	tty     bool
	lastLen int // length of last written line (for clearing)
}

// NewSpinner creates a Spinner that writes to stdout.
// If stdout is not a TTY or colors are disabled, it falls back to line-per-message output.
func NewSpinner() *Spinner {
	tty := !color.NoColor && isTerminal(os.Stdout)
	return &Spinner{
		w:   os.Stdout,
		tty: tty,
	}
}

// Start begins the spinner with an initial message.
func (s *Spinner) Start(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = true
	s.write(msg)
}

// Update replaces the spinner message.
func (s *Spinner) Update(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return
	}
	s.write(msg)
}

// Stop clears the spinner line and prints a final message (with newline).
func (s *Spinner) Stop(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = false
	if s.tty {
		s.clearLine()
	}
	if msg != "" {
		fmt.Fprintln(s.w, msg)
	}
}

// write outputs the message using \r overwrite (TTY) or plain println (non-TTY).
func (s *Spinner) write(msg string) {
	if s.tty {
		s.clearLine()
		n, _ := fmt.Fprintf(s.w, "\r%s", msg)
		s.lastLen = n
	} else {
		fmt.Fprintln(s.w, msg)
	}
}

// clearLine overwrites the previous line with spaces and returns the cursor.
func (s *Spinner) clearLine() {
	if s.lastLen > 0 {
		fmt.Fprintf(s.w, "\r%-*s\r", s.lastLen, "")
	}
}

// isTerminal reports whether f is a terminal.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
