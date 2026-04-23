package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newCompletionCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:       "completion [bash|fish|zsh|powershell]",
		Short:     "Generate shell completion script",
		ValidArgs: []string{"bash", "fish", "zsh", "powershell"},
		Args:      cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			switch args[0] {
			case "bash":
				return root.GenBashCompletion(out)
			case "fish":
				return root.GenFishCompletion(out, true)
			case "zsh":
				return root.GenZshCompletion(out)
			case "powershell":
				return root.GenPowerShellCompletion(out)
			default:
				return fmt.Errorf("unsupported shell %q: choose bash, fish, zsh, or powershell", args[0])
			}
		},
	}
}
