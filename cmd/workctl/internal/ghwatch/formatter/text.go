package formatter

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/ghwatch/event"
)

// Text writes human-readable, fixed-width output to w.
type Text struct {
	w io.Writer
}

// NewText returns a text formatter writing to w.
func NewText(w io.Writer) *Text {
	return &Text{w: w}
}

// Format writes a single event as a human-readable line.
func (t *Text) Format(e event.Event) error {
	ts := e.Timestamp.Local().Format(time.TimeOnly) // HH:MM:SS

	switch e.Kind {
	case event.KindPush:
		if e.Push != nil {
			if len(e.Push.Commits) == 0 {
				// Push event with no inline commits (payload truncated or empty).
				sha := shortSHA(e.Push.HeadSHA)
				_, err := fmt.Fprintf(t.w, "[%s] %-5s %-10s %s (%d commits, payload truncated)\n",
					ts, "PUSH", truncate(e.Push.Branch, 10), sha, e.Push.Size)
				return err
			}
			for _, c := range e.Push.Commits {
				msg := truncate(firstLine(c.Message), 50)
				_, err := fmt.Fprintf(t.w, "[%s] %-5s %-10s %s %q (%s)\n",
					ts, "PUSH", truncate(e.Push.Branch, 10), shortSHA(c.SHA), msg, c.Author)
				if err != nil {
					return err
				}
				// Show file changes if present.
				t.writeFiles("  + ", c.Added)
				t.writeFiles("  - ", c.Removed)
				t.writeFiles("  ~ ", c.Modified)
			}
			return nil
		}
	case event.KindPR:
		if e.PR != nil {
			title := truncate(e.PR.Title, 40)
			_, err := fmt.Fprintf(t.w, "[%s] %-5s #%-4d %-8s %q (%s)\n",
				ts, "PR", e.PR.Number, e.PR.Action, title, e.PR.Author)
			return err
		}
	case event.KindWorkflow:
		if e.Workflow != nil {
			status := e.Workflow.Status
			if e.Workflow.Conclusion != "" {
				status = status + "/" + e.Workflow.Conclusion
			}
			dur := ""
			if e.Workflow.Duration > 0 {
				dur = formatDuration(e.Workflow.Duration)
			}
			name := truncate(e.Workflow.Name, 12)
			branch := truncate(e.Workflow.Branch, 10)
			if dur != "" {
				_, err := fmt.Fprintf(t.w, "[%s] %-5s %-12s %-20s %-10s (%s)\n",
					ts, "CI", name, status, branch, dur)
				return err
			}
			_, err := fmt.Fprintf(t.w, "[%s] %-5s %-12s %-20s %s\n",
				ts, "CI", name, status, branch)
			return err
		}
	}

	// Fallback for events with missing detail.
	_, err := fmt.Fprintf(t.w, "[%s] %-5s %s\n", ts, strings.ToUpper(string(e.Kind)), e.ID)
	return err
}

// writeFiles writes file paths with a prefix indicator (e.g. "  + " for added).
func (t *Text) writeFiles(prefix string, files []string) {
	for _, f := range files {
		fmt.Fprintf(t.w, "%s%s\n", prefix, f)
	}
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%02ds", m, s)
}
