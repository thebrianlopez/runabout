package main

import (
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/blo-grindr/bmux/internal/config"
)

// DoctorCheck is a named diagnostic check.
type DoctorCheck struct {
	Name string
	Run  func() DoctorResult
}

// DoctorResult holds the outcome of a single diagnostic check.
type DoctorResult struct {
	OK      bool
	Message string
}

// defaultChecks returns the standard set of diagnostic checks.
func defaultChecks(paths *config.Paths) []DoctorCheck {
	return []DoctorCheck{
		{
			Name: "tmux-binary",
			Run: func() DoctorResult {
				path, err := exec.LookPath("tmux")
				if err != nil {
					return DoctorResult{OK: false, Message: "tmux not found in PATH"}
				}
				return DoctorResult{OK: true, Message: "tmux found at " + path}
			},
		},
		{
			Name: "node-binary",
			Run: func() DoctorResult {
				path, err := exec.LookPath("node")
				if err != nil {
					return DoctorResult{OK: false, Message: "node not found in PATH (required for xterm headless mirror — install Node.js ≥18)"}
				}
				return DoctorResult{OK: true, Message: "node found at " + path}
			},
		},
		{
			Name: "config-file",
			Run: func() DoctorResult {
				cfgPath := paths.ConfigFile()
				if _, err := config.LoadConfig(cfgPath); err != nil {
					return DoctorResult{OK: false, Message: fmt.Sprintf("config invalid: %v", err)}
				}
				return DoctorResult{OK: true, Message: "config valid at " + cfgPath}
			},
		},
	}
}

// newDoctorCmd creates the doctor subcommand using the standard checks.
func newDoctorCmd(paths *config.Paths) *cobra.Command {
	return newDoctorCmdWithChecks(defaultChecks(paths))
}

// newDoctorCmdWithChecks creates a doctor subcommand with injectable checks (for testing).
func newDoctorCmdWithChecks(checks []DoctorCheck) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run diagnostic checks",
		RunE: func(cmd *cobra.Command, args []string) error {
			allOK := true
			for _, check := range checks {
				result := check.Run()
				status := "✓"
				if !result.OK {
					status = "✗"
					allOK = false
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  %s %s: %s\n", status, check.Name, result.Message)
			}
			if !allOK {
				return fmt.Errorf("one or more doctor checks failed")
			}
			return nil
		},
	}
}
