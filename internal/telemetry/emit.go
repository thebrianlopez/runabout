package telemetry

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Tracker records CLI invocation timing and emits telemetry via emit_jsonl.
type Tracker struct {
	cliName string
	start   time.Time
	cmd     *cobra.Command
}

// Instrument adds a PersistentPreRun hook to rootCmd that captures the start
// time and resolved subcommand. Call Emit after rootCmd.Execute completes.
func Instrument(rootCmd *cobra.Command, cliName string) *Tracker {
	t := &Tracker{cliName: cliName}

	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		t.start = time.Now()
		t.cmd = cmd
	}

	return t
}

// Emit sends a schema v2 JSONL event via fish subprocess. Emission failure
// is non-fatal: errors are logged to stderr but do not affect the caller.
func (t *Tracker) Emit(cmdErr error) {
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

	fishCmd, err := buildEmitCmd(t.cliName, subcmd, duration, exitCode, flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "telemetry: %v\n", err)
		return
	}

	if err := exec.Command("fish", "-c", fishCmd).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "telemetry: emit_jsonl: %v\n", err)
	}
}

// buildEmitCmd constructs the fish command string for emit_jsonl.
func buildEmitCmd(cliName, subcmd string, durationMs int64, exitCode int, flags map[string]string) (string, error) {
	metadata := map[string]interface{}{
		"flags": flags,
	}
	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("marshal metadata: %w", err)
	}

	escapedMeta := strings.ReplaceAll(string(metaJSON), "'", "'\\''")

	return fmt.Sprintf(
		"emit_jsonl --layer go_cli --event-type command --command '%s %s' --duration-ms %d --exit-code %d --metadata-json '%s'",
		cliName, subcmd, durationMs, exitCode, escapedMeta,
	), nil
}
