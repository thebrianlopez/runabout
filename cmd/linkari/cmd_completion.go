package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// completionCmd generates shell completion scripts using Cobra's built-in
// generators. Install via:
//
//	linkari completion fish > ~/.config/fish/completions/linkari.fish
//
// Fish auto-loads anything in ~/.config/fish/completions/ — no edit to
// config.fish required.
func completionCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:       "completion [bash|zsh|fish|powershell]",
		Short:     "Generate shell completion script",
		Long:      `Generate a shell completion script for the named shell. Pipe output into the appropriate completion directory for your shell.`,
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return root.GenBashCompletion(os.Stdout)
			case "zsh":
				return root.GenZshCompletion(os.Stdout)
			case "fish":
				return root.GenFishCompletion(os.Stdout, true)
			case "powershell":
				return root.GenPowerShellCompletionWithDesc(os.Stdout)
			}
			return fmt.Errorf("unsupported shell: %s", args[0])
		},
	}
}

// completeProfiles returns the set of valid profile names by reading the
// loaded action config. It is wired as a flag completion func via
// RegisterFlagCompletionFunc and runs in the user's shell during <tab>.
//
// Source of truth: actions in the loaded YAML config whose ProfileMap == "prefix".
// The profile name is the suffix after "uinit_" in the action ID. Falls back to
// the legacy hardcoded set if config can't be loaded — completions should never
// crash the shell.
func completeProfiles(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cfg, err := LoadConfig(context.Background(), os.Getenv("LINKARI_CONFIG"))
	if err != nil {
		// Fallback to built-in profile set so completions still work without config.
		cfg = builtinConfig()
	}

	seen := make(map[string]bool)
	var profiles []string
	for _, a := range cfg.Actions {
		if a.ProfileMap != "prefix" {
			continue
		}
		suffix := strings.TrimPrefix(a.ID, "uinit_")
		if suffix == "" || suffix == a.ID || seen[suffix] {
			continue
		}
		seen[suffix] = true
		profiles = append(profiles, suffix)
	}

	if len(profiles) == 0 {
		profiles = []string{"eng", "life", "travel", "fashion", "music", "finance", "dining"}
	}

	return profiles, cobra.ShellCompDirectiveNoFileComp
}

// registerCompletions wires dynamic flag completers onto every subcommand
// that exposes a --profile flag. Called from main() after all subcommands
// have been added to the root command.
func registerCompletions(root *cobra.Command) {
	for _, sub := range root.Commands() {
		if sub.Flags().Lookup("profile") != nil {
			_ = sub.RegisterFlagCompletionFunc("profile", completeProfiles)
		}
	}
}
