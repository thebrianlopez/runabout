package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	version = "0.1.0"
	commit  = "dev"
	date    = "unknown"
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "protonexport",
		Short:   "Export ProtonMail conversations to markdown",
		Version: fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date),
	}

	rootCmd.AddCommand(exportCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func exportCmd() *cobra.Command {
	var (
		username  string
		password  string
		contact   string
		outputDir string
		workers   int
	)

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export emails matching a contact to markdown files",
		Long: `Authenticate with ProtonMail, fetch all messages involving the specified
contact email (as sender or recipient), decrypt them, and write each
message as a markdown file with YAML front-matter.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if username == "" {
				username = os.Getenv("PROTON_USERNAME")
			}
			if password == "" {
				password = os.Getenv("PROTON_PASSWORD")
			}
			if contact == "" {
				contact = os.Getenv("PROTON_SENDER")
			}
			if outputDir == "" {
				outputDir = os.Getenv("PROTON_OUTPUT_DIR")
			}
			if outputDir == "" {
				outputDir = "./export"
			}

			if username == "" || password == "" || contact == "" {
				return fmt.Errorf("required: --username, --password, --contact (or PROTON_USERNAME, PROTON_PASSWORD, PROTON_SENDER env vars)")
			}

			return runExport(exportConfig{
				username:    username,
				password:    password,
				senderEmail: contact,
				outputDir:   outputDir,
				workers:     workers,
			})
		},
	}

	cmd.Flags().StringVarP(&username, "username", "u", "", "ProtonMail username (env: PROTON_USERNAME)")
	cmd.Flags().StringVarP(&password, "password", "p", "", "ProtonMail password (env: PROTON_PASSWORD)")
	cmd.Flags().StringVarP(&contact, "contact", "c", "", "email address to filter conversations (env: PROTON_SENDER)")
	cmd.Flags().StringVarP(&outputDir, "output", "o", "", "output directory (default: ./export, env: PROTON_OUTPUT_DIR)")
	cmd.Flags().IntVarP(&workers, "workers", "w", 10, "number of concurrent export workers")

	return cmd
}
