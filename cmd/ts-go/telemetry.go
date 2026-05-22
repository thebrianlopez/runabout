package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
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

	ev := buildEvent(t.cliName, subcmd, duration, exitCode, flags)

	if ev.EventClass == "hook" && (!hookCWDAllowed(ev.CWD) || isHookRateLimited(ev.Command, ev.CWD)) {
		return
	}

	if err := writeEvent(ev); err != nil {
		fmt.Fprintf(os.Stderr, "telemetry: %v\n", err)
	}
}

// hookSubcmds is the set of ts-go subcommand names that are shell-integration
// hooks rather than user-intent invocations. These fire on every prompt render
// or tab completion and must be rate-limited to avoid bus saturation.
var hookSubcmds = map[string]bool{
	"fish":             true,
	"__complete":       true,
	"__completeNoDesc": true,
}

// hookRateLimitTTL is the minimum interval between emitted hook-class events
// for a given (command, cwd) pair.
const hookRateLimitTTL = 60 * time.Second

// event is a schema v2 JSONL telemetry record.
type event struct {
	SchemaVersion string                 `json:"schema_version"`
	Timestamp     string                 `json:"timestamp"`
	Layer         string                 `json:"layer"`
	EventType     string                 `json:"event_type"`
	EventClass    string                 `json:"event_class"`
	Command       string                 `json:"command"`
	SessionID     string                 `json:"session_id"`
	User          string                 `json:"user"`
	CWD           string                 `json:"cwd"`
	SourceMachine string                 `json:"source_machine"`
	DurationMs    int64                  `json:"duration_ms"`
	ExitCode      int                    `json:"exit_code"`
	Agent         *string                `json:"agent"`
	Epic          *string                `json:"epic"`
	Milestone     *string                `json:"milestone"`
	Metadata      map[string]interface{} `json:"metadata"`
}

// classifySubcmd returns "hook" for shell-integration subcommands, "user_intent" otherwise.
func classifySubcmd(subcmd string) string {
	if hookSubcmds[subcmd] {
		return "hook"
	}
	return "user_intent"
}

// hookCWDAllowed returns false for the user's home directory. Hook events from
// home are terminal-startup artifacts (fish opens in ~ before any cd) rather
// than command-use signal, so they are suppressed unconditionally.
func hookCWDAllowed(cwd string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return true
	}
	return cwd != home
}

// hookRateLimitSentinel returns the path to the per-(command,cwd) sentinel file.
func hookRateLimitSentinel(command, cwd string) string {
	h := fnv.New32a()
	h.Write([]byte(command + "|" + cwd))
	key := fmt.Sprintf("%08x", h.Sum32())
	return filepath.Join(os.TempDir(), "automation-metrics-rl", key)
}

// isHookRateLimited returns true if a hook-class event for (command, cwd) was
// emitted within the last hookRateLimitTTL. If not, it atomically claims the
// sentinel so concurrent duplicate callers are suppressed.
func isHookRateLimited(command, cwd string) bool {
	sentinel := hookRateLimitSentinel(command, cwd)
	if err := os.MkdirAll(filepath.Dir(sentinel), 0o755); err != nil {
		return false
	}
	if info, err := os.Stat(sentinel); err == nil {
		if time.Since(info.ModTime()) < hookRateLimitTTL {
			return true // within TTL
		}
		os.Remove(sentinel) //nolint:errcheck — stale, remove before atomic re-create
	}
	// O_EXCL: only one concurrent caller succeeds; losers are treated as
	// rate-limited to suppress the duplicate-pair emission from two hook registrations.
	f, err := os.OpenFile(sentinel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return true // lost the race — suppress this duplicate
	}
	f.Close()
	return false
}

// buildEvent constructs a schema v2 event struct.
func buildEvent(cliName, subcmd string, durationMs int64, exitCode int, flags map[string]string) event {
	cwd, _ := os.Getwd()
	user := os.Getenv("USER")
	sid := os.Getenv("CLAUDE_CODE_SESSION_ID")
	if sid == "" {
		sid = os.Getenv("__fish_session_id")
	}
	if sid == "" {
		sid = "unknown"
	}

	hostname, _ := os.Hostname()

	return event{
		SchemaVersion: "2",
		Timestamp:     time.Now().UTC().Format("20060102T150405Z"),
		Layer:         "go_cli",
		EventType:     "command",
		EventClass:    classifySubcmd(subcmd),
		Command:       cliName + " " + subcmd,
		SessionID:     sid,
		User:          user,
		CWD:           cwd,
		SourceMachine: hostname,
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

// sensitivePatterns lists flag name substrings whose values must be redacted.
var sensitivePatterns = []string{"authkey", "token", "secret", "password", "key", "query", "pattern"}

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
