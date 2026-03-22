package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/blo-grindr/runabout/internal/mdq"
	"github.com/blo-grindr/runabout/internal/telemetry"
	versionpkg "github.com/blo-grindr/runabout/internal/version"
	"github.com/spf13/cobra"
)

var (
	version = "0.1.0"
	commit  = "dev"
	date    = "unknown"
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "mdq",
		Short:   "Markdown query tool with structured selectors",
		Version: versionpkg.Format(version, commit, date),
	}

	rootCmd.AddCommand(queryCmd())
	rootCmd.AddCommand(tableCmd())
	rootCmd.AddCommand(extractCmd())
	rootCmd.AddCommand(listCmd())

	t := telemetry.Instrument(rootCmd, "mdq")
	err := rootCmd.Execute()
	t.Emit(err)
	if err != nil {
		os.Exit(1)
	}
}

func queryCmd() *cobra.Command {
	var field, table, format string

	cmd := &cobra.Command{
		Use:   "query <glob>",
		Short: "Query fields across markdown files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := mdq.Query{Field: field, Table: table}
			results, err := mdq.Execute(args[0], q)
			if err != nil {
				return err
			}
			fmt.Print(mdq.Format(results, format))
			return nil
		},
	}

	cmd.Flags().StringVar(&field, "field", "", "field name to query")
	cmd.Flags().StringVar(&table, "table", "", "table section to search within")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text, json, table")
	_ = cmd.MarkFlagRequired("field")

	return cmd
}

func tableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "table <file> <section>",
		Short: "Extract a table from a markdown section",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer f.Close()

			doc, err := mdq.Parse(f)
			if err != nil {
				return err
			}

			sec := mdq.FindSection(doc, args[1])
			if sec == nil {
				return fmt.Errorf("section %q not found", args[1])
			}

			if len(sec.Tables) == 0 {
				return fmt.Errorf("no tables found in section %q", args[1])
			}

			for _, t := range sec.Tables {
				fmt.Print(mdq.FormatTable(t))
			}
			return nil
		},
	}
}

func extractCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "extract <file> <section>",
		Short: "Extract section content from a markdown file",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer f.Close()

			doc, err := mdq.Parse(f)
			if err != nil {
				return err
			}

			sec := mdq.FindSection(doc, args[1])
			if sec == nil {
				return fmt.Errorf("section %q not found", args[1])
			}

			fmt.Println(mdq.FormatSection(sec))
			return nil
		},
	}
}

func listCmd() *cobra.Command {
	var headings bool
	var level int
	var format string
	var exclude string
	var groupBy string

	cmd := &cobra.Command{
		Use:   "list <glob>",
		Short: "List headings across markdown files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := mdq.Query{Level: level}
			if headings {
				q.Heading = "*"
			}

			opts := mdq.ListOptions{GroupBy: groupBy}
			if exclude != "" {
				opts.Exclude = strings.Split(exclude, ",")
			}

			results, err := mdq.ExecuteWithOptions(args[0], q, opts)
			if err != nil {
				return err
			}

			if opts.GroupBy == "dir" {
				groups := mdq.GroupByDir(results)
				fmt.Print(mdq.FormatGrouped(groups, format))
			} else {
				fmt.Print(mdq.Format(results, format))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&headings, "headings", false, "list headings only")
	cmd.Flags().IntVar(&level, "level", 0, "filter by heading level")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text, json, table")
	cmd.Flags().StringVar(&exclude, "exclude", "", "comma-separated directory names to skip")
	cmd.Flags().StringVar(&groupBy, "group-by", "", "group results (supported: dir)")

	return cmd
}
