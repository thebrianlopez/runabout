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

const maxEventFileSize = 52428800

type tracker struct {
	cliName string
	start   time.Time
	cmd     *cobra.Command
}

func instrument(rootCmd *cobra.Command, cliName string) *tracker {
	t := &tracker{cliName: cliName}
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		t.start = time.Now()
		t.cmd = cmd
	}
	return t
}

func (t *tracker) emit(cmdErr error) {
	if t.cmd == nil {
		return
	}
	exitCode := 0
	if cmdErr != nil {
		exitCode = 1
	}
	flags := map[string]string{}
	t.cmd.Flags().Visit(func(f *pflag.Flag) { flags[f.Name] = f.Value.String() })

	e := cliEvent{
		SchemaVersion: "2",
		Timestamp:     time.Now().UTC().Format("20060102T150405Z"),
		Layer:         "go_cli",
		EventType:     "command",
		Command:       t.cliName + " " + t.cmd.Name(),
		SessionID:     sessionID(),
		User:          os.Getenv("USER"),
		CWD:           cwd(),
		DurationMs:    time.Since(t.start).Milliseconds(),
		ExitCode:      exitCode,
		Metadata:      map[string]any{"flags": scrubFlags(flags)},
	}

	data, _ := json.Marshal(e)
	data = append(data, '\n')

	dir := eventsDir()
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, time.Now().Format("2006-01-02")+".jsonl")

	if info, err := os.Stat(path); err == nil && info.Size() > maxEventFileSize {
		for n := 1; ; n++ {
			rotated := fmt.Sprintf("%s.%d", path, n)
			if _, err := os.Stat(rotated); os.IsNotExist(err) {
				os.Rename(path, rotated)
				break
			}
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "telemetry: %v\n", err)
		return
	}
	defer f.Close()
	f.Write(data)
}

type cliEvent struct {
	SchemaVersion string         `json:"schema_version"`
	Timestamp     string         `json:"timestamp"`
	Layer         string         `json:"layer"`
	EventType     string         `json:"event_type"`
	Command       string         `json:"command"`
	SessionID     string         `json:"session_id"`
	User          string         `json:"user"`
	CWD           string         `json:"cwd"`
	DurationMs    int64          `json:"duration_ms"`
	ExitCode      int            `json:"exit_code"`
	Metadata      map[string]any `json:"metadata"`
}

var sensitivePatterns = []string{"authkey", "token", "secret", "password", "key"}

func scrubFlags(flags map[string]string) map[string]string {
	out := make(map[string]string, len(flags))
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
			out[name] = "[REDACTED]"
		} else {
			out[name] = val
		}
	}
	return out
}

func sessionID() string {
	sid := os.Getenv("__fish_session_id")
	if sid == "" {
		return "unknown"
	}
	return sid
}

func cwd() string {
	d, _ := os.Getwd()
	return d
}
