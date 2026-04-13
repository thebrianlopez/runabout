package export

import (
	"context"
	"fmt"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/api"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/insights"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/pipeline"
)

// ConfluentSinkConfig holds configuration for a ConfluentSink.
type ConfluentSinkConfig struct {
	// Clients is the initialized Atlassian API client.
	Clients *api.AtlassianClients
	// SpaceKey is the Confluence space key (e.g. "~accountId" or "TEAM").
	SpaceKey string
	// AncestorID is the parent page or folder ID under which the page is created.
	AncestorID string
	// AuthorName is the display name inserted into the page header.
	AuthorName string
}

// ConfluentSink publishes a standup report to Confluence.
// It must be placed AFTER FileSink in a MultiSink to preserve the
// file-canonical record model: the local file is the authoritative record;
// Confluence is a distribution layer.
//
// A failure in ConfluentSink returns a non-zero exit code to the caller but
// leaves any previously written local files intact.
type ConfluentSink struct {
	cfg ConfluentSinkConfig
}

// NewConfluentSink returns a Sink that publishes reports to Confluence.
func NewConfluentSink(cfg ConfluentSinkConfig) *ConfluentSink {
	return &ConfluentSink{cfg: cfg}
}

// Name implements pipeline.Sink.
func (s *ConfluentSink) Name() string { return "confluence" }

// Write publishes the report to Confluence using the pre-rendered HTML in rd.HTML.
// If rd.HTML is empty, Write is a no-op (returns nil) — callers must set HTML
// before passing ReportData to a MultiSink that includes a ConfluentSink.
func (s *ConfluentSink) Write(ctx context.Context, rd *pipeline.ReportData) error {
	if rd.HTML == "" {
		return nil
	}
	if s.cfg.Clients == nil {
		return fmt.Errorf("confluence sink: no Atlassian client configured")
	}
	if s.cfg.SpaceKey == "" || s.cfg.AncestorID == "" {
		return fmt.Errorf("confluence sink: SpaceKey and AncestorID are required")
	}

	title := insights.FormatStandupTitle(rd.PeriodStart, rd.PeriodEnd)
	_, pageURL, err := s.cfg.Clients.PublishPage(ctx, s.cfg.SpaceKey, s.cfg.AncestorID, title, rd.HTML)
	if err != nil {
		return fmt.Errorf("confluence sink: publish failed: %w", err)
	}

	fmt.Printf("Published standup to Confluence: %s\n", pageURL)
	return nil
}
