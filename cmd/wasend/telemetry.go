package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// maxEventFileSize is the rotation threshold (50MB, matching emit_jsonl.fish).
const maxEventFileSize = 52428800

// tracker records CLI invocation timing and emits telemetry as native JSONL.
type tracker struct {
	cliName string
	start   time.Time
	cmd     *cobra.Command
}

// instrument adds a PersistentPreRun hook to rootCmd that captures the start
// time and resolved subcommand. Call emit after rootCmd.Execute completes.
func instrument(rootCmd *cobra.Command, cliName string) *tracker {
	t := &tracker{cliName: cliName}

	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		t.start = time.Now()
		t.cmd = cmd
	}

	return t
}

// emit writes a schema v2 JSONL event directly to the events directory.
// Emission failure is non-fatal: errors are logged to stderr.
func (t *tracker) emit(cmdErr error) {
	if t.cmd == nil {
		return
	}

	exitCode := 0
	if cmdErr != nil {
		exitCode = 1
	}

	duration := time.Since(t.start).Milliseconds()
	subcmd := t.cmd.Name()

	flags := map[string]string{}
	t.cmd.Flags().Visit(func(f *pflag.Flag) {
		flags[f.Name] = f.Value.String()
	})

	event := buildEvent(t.cliName, subcmd, duration, exitCode, flags)

	if err := writeEvent(event); err != nil {
		fmt.Fprintf(os.Stderr, "telemetry: %v\n", err)
	}
}

// event is a schema v2 JSONL telemetry record.
type event struct {
	SchemaVersion string                 `json:"schema_version"`
	Timestamp     string                 `json:"timestamp"`
	Layer         string                 `json:"layer"`
	EventType     string                 `json:"event_type"`
	Command       string                 `json:"command"`
	SessionID     string                 `json:"session_id"`
	User          string                 `json:"user"`
	CWD           string                 `json:"cwd"`
	DurationMs    int64                  `json:"duration_ms"`
	ExitCode      int                    `json:"exit_code"`
	Agent         *string                `json:"agent"`
	Epic          *string                `json:"epic"`
	Milestone     *string                `json:"milestone"`
	Metadata      map[string]interface{} `json:"metadata"`
}

// buildEvent constructs a schema v2 event struct.
func buildEvent(cliName, subcmd string, durationMs int64, exitCode int, flags map[string]string) event {
	cwd, _ := os.Getwd()
	user := os.Getenv("USER")
	sid := os.Getenv("__fish_session_id")
	if sid == "" {
		sid = "unknown"
	}

	return event{
		SchemaVersion: "2",
		Timestamp:     time.Now().UTC().Format("20060102T150405Z"),
		Layer:         "go_cli",
		EventType:     "command",
		Command:       cliName + " " + subcmd,
		SessionID:     sid,
		User:          user,
		CWD:           cwd,
		DurationMs:    durationMs,
		ExitCode:      exitCode,
		Metadata: map[string]interface{}{
			"flags": scrubFlags(flags),
		},
	}
}

// eventsDir returns the events directory path, respecting AUTOMATION_METRICS_DIR.
func eventsDir() string {
	base := os.Getenv("AUTOMATION_METRICS_DIR")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".automation-metrics")
	}
	return filepath.Join(base, "events")
}

// writeEvent marshals and appends an event to the daily JSONL file.
func writeEvent(e event) error {
	dir := eventsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create events dir: %w", err)
	}

	dateStr := time.Now().Format("2006-01-02")
	path := filepath.Join(dir, dateStr+".jsonl")

	// Log rotation: if file exceeds 50MB, rename with numeric suffix.
	if info, err := os.Stat(path); err == nil && info.Size() > maxEventFileSize {
		n := 1
		for {
			rotated := fmt.Sprintf("%s.%d", path, n)
			if _, err := os.Stat(rotated); os.IsNotExist(err) {
				os.Rename(path, rotated)
				break
			}
			n++
		}
	}

	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	data = append(data, '\n')

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open events file: %w", err)
	}
	defer f.Close()

	_, err = f.Write(data)
	return err
}

// buildEmitCmd constructs a beads emit shell command string for the given
// invocation parameters. The returned string is safe for eval in a POSIX shell
// — all single-quoted arguments use '\” escaping for embedded single quotes.
func buildEmitCmd(cliName, subcmd string, durationMs int64, exitCode int, flags map[string]string) (string, error) {
	metaJSON, err := json.Marshal(map[string]interface{}{"flags": scrubFlags(flags)})
	if err != nil {
		return "", fmt.Errorf("marshal flags: %w", err)
	}
	sq := func(s string) string {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return fmt.Sprintf(
		"beads emit --layer go_cli --event-type command --command %s --duration-ms %d --exit-code %d %s",
		sq(cliName+" "+subcmd), durationMs, exitCode, sq(string(metaJSON)),
	), nil
}

// sensitivePatterns lists flag name substrings whose values must be redacted.
var sensitivePatterns = []string{"authkey", "token", "secret", "password", "key"}

// scrubFlags returns a copy of flags with sensitive values replaced by [REDACTED].
func scrubFlags(flags map[string]string) map[string]string {
	scrubbed := make(map[string]string, len(flags))
	for name, val := range flags {
		lower := strings.ToLower(name)
		redact := false
		for _, pat := range sensitivePatterns {
			if strings.Contains(lower, pat) {
				redact = true
				break
			}
		}
		if redact {
			scrubbed[name] = "[REDACTED]"
		} else {
			scrubbed[name] = val
		}
	}
	return scrubbed
}
