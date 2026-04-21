package main

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// allBuildClaudeArgsFlags returns every distinct --flag name that buildClaudeArgs
// can emit across all scoring paths. This list MUST be updated whenever
// buildClaudeArgs gains or loses a flag — the contract test validates each entry
// against the installed claude binary's --help output.
func allBuildClaudeArgsFlags() []string {
	return []string{
		"--print",
		"--model",
		"--max-turns",
		"--tools",
		"--allowedTools",
		"--output-format",
		"--json-schema",
		"--system-prompt-file",
		"--effort",
		"--no-session-persistence",
	}
}

// deprecatedClaudeFlags lists flags that were removed from the Claude CLI and
// must never appear in buildClaudeArgs output. Add entries here when the CLI
// drops a flag we previously used.
var deprecatedClaudeFlags = []string{
	"--max-tokens", // removed 2026-04; replaced by --max-budget-usd
}

// undocumentedClaudeFlags lists flags that are accepted by the CLI but not shown
// in --help output. These are verified to work but are excluded from the --help
// parse check. If a flag here stops working, move it to deprecatedClaudeFlags.
var undocumentedClaudeFlags = map[string]bool{
	"--max-turns": true, // hidden flag; works as of claude 2.1.104
}

// parseClaudeHelpFlags extracts the set of valid --flag names from claude --help
// output. It expands bracket-variant notation used by the CLI's help formatter:
// --foo[-bar] produces both --foo and --foo-bar in the returned set.
// Example: --system-prompt[-file] → {"--system-prompt", "--system-prompt-file"}.
func parseClaudeHelpFlags(helpOutput string) map[string]bool {
	valid := make(map[string]bool)
	// Match --flag-name with optional [-suffix] bracket variant.
	re := regexp.MustCompile(`--([a-zA-Z][a-zA-Z0-9-]*)(?:\[-([a-zA-Z0-9-]+)\])?`)
	for _, match := range re.FindAllStringSubmatch(helpOutput, -1) {
		base := "--" + match[1]
		valid[base] = true
		if match[2] != "" {
			valid[base+"-"+match[2]] = true
		}
	}
	return valid
}

// TestClaudeCLIFlagContract validates every flag buildClaudeArgs can emit against
// the real installed claude binary's --help output. Skips when claude is not on
// PATH (CI-safe). Run locally before pushing to main.
//
// This is a contract test: it catches flag removals, renames, and use of
// deprecated flags at test time rather than at runtime in production.
func TestClaudeCLIFlagContract(t *testing.T) {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude binary not on PATH — skipping contract test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, claudePath, "--help")
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		t.Fatalf("claude --help failed with no output: %v", err)
	}
	helpOutput := string(out)

	validFlags := parseClaudeHelpFlags(helpOutput)
	if len(validFlags) < 5 {
		t.Fatalf("parseClaudeHelpFlags returned only %d flags — help output format may have changed", len(validFlags))
	}

	t.Run("version_flag_present", func(t *testing.T) {
		// validateClaudeCLI uses --version at startup.
		if !validFlags["--version"] {
			t.Error("--version not found in claude --help output")
		}
	})

	t.Run("build_args_flags_all_valid", func(t *testing.T) {
		for _, flag := range allBuildClaudeArgsFlags() {
			if undocumentedClaudeFlags[flag] {
				continue // verified separately in undocumented_flags_still_accepted
			}
			if !validFlags[flag] {
				t.Errorf("flag %q used by buildClaudeArgs is not in claude --help output (removed or renamed?)", flag)
			}
		}
	})

	t.Run("undocumented_flags_still_accepted", func(t *testing.T) {
		// Smoke-test that each undocumented flag is still accepted by the binary.
		// Uses --help as a no-op invocation that validates flag parsing without
		// making an API call. If the flag was removed, claude exits non-zero with
		// "unknown option".
		for flag := range undocumentedClaudeFlags {
			ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
			// claude --print --max-turns 1 --help should exit 0 if --max-turns is valid.
			cmd2 := exec.CommandContext(ctx2, claudePath, flag, "1", "--help")
			out2, err2 := cmd2.CombinedOutput()
			cancel2()
			output := string(out2)
			if err2 != nil && strings.Contains(output, "unknown option") {
				t.Errorf("undocumented flag %q is no longer accepted by the claude binary — move to deprecatedClaudeFlags", flag)
			}
		}
	})

	t.Run("no_deprecated_flags_emitted", func(t *testing.T) {
		// Exercise all three scoring paths and assert no deprecated flags appear.
		paths := []struct {
			name string
			opts claudeExecOpts
		}{
			{"json", claudeExecOpts{
				Model: "m", MaxTurns: "3", Tools: "",
				OutputFormat: "json", JSONSchema: "{}", SystemPrompt: "/tmp/sp",
			}},
			{"vision", claudeExecOpts{
				Model: "m", MaxTurns: "3", AllowedTools: "Read",
				OutputFormat: "json", JSONSchema: "{}", SystemPrompt: "/tmp/sp",
			}},
			{"plain_text", claudeExecOpts{
				Model: "m", MaxTurns: "1", Tools: "", SystemPrompt: "/tmp/sp",
			}},
		}
		for _, p := range paths {
			args := buildClaudeArgs(p.opts)
			for _, dep := range deprecatedClaudeFlags {
				for _, a := range args {
					if a == dep {
						t.Errorf("deprecated flag %q emitted by buildClaudeArgs (path=%s): %s",
							dep, p.name, strings.Join(args, " "))
					}
				}
			}
		}
	})
}

// TestParseClaudeHelpFlags verifies the bracket-expansion parser handles the
// known edge cases in claude --help output.
func TestParseClaudeHelpFlags(t *testing.T) {
	sample := `  --model <model>                                   Model for the current session.
  --system-prompt <prompt>                          System prompt to use for the session
  --bare                                            Minimal mode: --system-prompt[-file], --append-system-prompt[-file]
  --allowedTools, --allowed-tools <tools...>        Comma or space-separated list
  --no-session-persistence                          Disable session persistence
  --effort <level>                                  Effort level (low, medium, high, max)
  --json-schema <schema>                            JSON Schema for structured output
  --output-format <format>                          Output format
  --max-turns <n>                                   Max turns`

	flags := parseClaudeHelpFlags(sample)

	want := []string{
		"--model", "--system-prompt", "--system-prompt-file",
		"--append-system-prompt", "--append-system-prompt-file",
		"--allowedTools", "--allowed-tools",
		"--no-session-persistence", "--effort",
		"--json-schema", "--output-format", "--max-turns",
		"--bare",
	}
	for _, f := range want {
		if !flags[f] {
			t.Errorf("expected %q in parsed flags, got: %v", f, flags)
		}
	}

	// Ensure bracket expansion doesn't produce garbage.
	if flags["--system-prompt-[-file]"] {
		t.Error("bracket notation was not expanded correctly")
	}
}
