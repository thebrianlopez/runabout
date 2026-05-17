package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/spf13/cobra"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/api"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/export"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/insights"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/pipeline"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/ui"
	"github.com/thebrianlopez/runabout/cmd/workctl/internal/version"
)

// runStandupPublish renders a standup HTML page from rd and either prints it
// to stdout (--dry-run) or publishes it to Confluence (--publish).
// Called from runWeekly when either flag is set.
func runStandupPublish(cmd *cobra.Command, rd *ReportData) error {
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	// Resolve author name: flag > config > derived from email
	authorName, _ := cmd.Flags().GetString("standup-author")
	if authorName == "" {
		authorName = resolved.StandupAuthor
	}
	if authorName == "" && resolved.Email != "" {
		authorName = nameFromEmail(resolved.Email)
	}
	if authorName == "" {
		authorName = "Unknown Author"
	}

	// Load optional notes sidecar
	notesFile, _ := cmd.Flags().GetString("standup-notes")
	var notes *insights.StandupNotes
	if notesFile != "" {
		var err error
		notes, err = loadStandupNotes(notesFile)
		if err != nil {
			return fmt.Errorf("loading standup notes: %w", err)
		}
	}

	// Render HTML
	var buf bytes.Buffer
	opts := insights.StandupOpts{
		AuthorName: authorName,
		Period:     rd.Period,
		Start:      rd.Start,
		End:        rd.End,
		Issues:     rd.Issues,
		Activities: rd.Activities,
		Signals:    rd.Signals,
		Notes:      notes,
		Generated:  rd.Generated,
		Version:    version.Get().Version,
	}
	if err := insights.RenderStandupHTML(&buf, opts); err != nil {
		return fmt.Errorf("rendering standup HTML: %w", err)
	}

	if dryRun {
		fmt.Print(buf.String())
		ui.Infof("(dry-run) Standup HTML printed to stdout — Confluence publish skipped\n")
		return nil
	}

	// Resolve publish targets: flag > config
	spaceKey, _ := cmd.Flags().GetString("confluence-space-key")
	if spaceKey == "" {
		spaceKey = resolved.ConfluenceSpaceKey
	}
	folderID, _ := cmd.Flags().GetString("confluence-folder-id")
	if folderID == "" {
		folderID = resolved.ConfluenceFolderID
	}
	if spaceKey == "" || folderID == "" {
		return fmt.Errorf("--confluence-space-key and --confluence-folder-id are required for --publish\n" +
			"  (or set confluence_space_key / confluence_folder_id in defaults: in your config file)")
	}

	// Initialise Atlassian client.
	domain := envOrConfig(resolved.AtlassianDomain, "ATLASSIAN_DOMAIN")
	email := envOrConfig(resolved.AtlassianEmail, "ATLASSIAN_EMAIL")
	token := envOrConfig(resolved.AtlassianToken, "ATLASSIAN_API_TOKEN")
	if domain == "" || email == "" || token == "" {
		return fmt.Errorf("Atlassian credentials required for --publish (ATLASSIAN_DOMAIN, ATLASSIAN_EMAIL, ATLASSIAN_API_TOKEN)")
	}

	clients, err := api.NewAtlassianClients(domain, email, token)
	if err != nil {
		return fmt.Errorf("initializing Atlassian client: %w", err)
	}

	// Build pipeline.ReportData with pre-rendered HTML.
	// File-first: FileSink writes .signals.json, then ConfluentSink publishes.
	prd := toPipelineReportData(rd)
	prd.HTML = buf.String()

	sinks := pipeline.NewMultiSink(
		export.NewFileSink(),
		export.NewConfluentSink(export.ConfluentSinkConfig{
			Clients:    clients,
			SpaceKey:   spaceKey,
			AncestorID: folderID,
			AuthorName: authorName,
		}),
	)

	ctx := context.Background()
	if err := sinks.Write(ctx, prd); err != nil {
		return fmt.Errorf("standup publish: %w", err)
	}

	ui.Successf("Published standup to Confluence\n")
	return nil
}

// nameFromEmail derives a display name from an email address.
// "brian.lopez@example.com" → "Brian Lopez"
func nameFromEmail(email string) string {
	local := strings.Split(email, "@")[0]
	parts := strings.Split(local, ".")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

// loadStandupNotes reads and parses a YAML sidecar file for standup narrative sections.
func loadStandupNotes(path string) (*insights.StandupNotes, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var notes insights.StandupNotes
	if err := yaml.Unmarshal(data, &notes); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &notes, nil
}
