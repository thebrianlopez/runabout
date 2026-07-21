package main

import (
	"context"
	"fmt"

	"github.com/thebrianlopez/runabout/internal/firecrawl"
)

// firecrawlClient is the process-level Firecrawl client. nil until
// initFirecrawlClient is called successfully.
var firecrawlClient *firecrawl.FirecrawlApp

// initFirecrawlClient initializes the singleton Firecrawl client from cfg.
// Returns an error if api_key is empty (client will remain nil and
// fetchFirecrawlContent will return an error on each call).
func initFirecrawlClient(cfg FirecrawlConfig) error {
	if cfg.APIKey == "" {
		return fmt.Errorf("firecrawl: api_key is not set")
	}
	app, err := firecrawl.NewFirecrawlApp(cfg.APIKey, cfg.APIURL)
	if err != nil {
		return fmt.Errorf("firecrawl: init client: %w", err)
	}
	firecrawlClient = app
	return nil
}

// fetchFirecrawlContent retrieves page content via the Firecrawl scrape API.
// Returns the markdown representation of the page. Honors ctx for cancellation.
func fetchFirecrawlContent(ctx context.Context, rawURL string) (string, error) {
	if firecrawlClient == nil {
		return "", fmt.Errorf("firecrawl: client not initialized")
	}

	onlyMain := true
	params := &firecrawl.ScrapeParams{
		OnlyMainContent: &onlyMain,
		Formats:         []string{"markdown"},
	}

	type result struct {
		doc *firecrawl.FirecrawlDocument
		err error
	}
	ch := make(chan result, 1)
	go func() {
		doc, err := firecrawlClient.ScrapeURL(rawURL, params)
		ch <- result{doc, err}
	}()

	select {
	case <-ctx.Done():
		return "", fmt.Errorf("firecrawl: context cancelled: %w", ctx.Err())
	case r := <-ch:
		if r.err != nil {
			return "", fmt.Errorf("firecrawl: scrape %s: %w", rawURL, r.err)
		}
		if r.doc == nil || r.doc.Markdown == "" {
			return "", fmt.Errorf("firecrawl: empty markdown for %s", rawURL)
		}
		return r.doc.Markdown, nil
	}
}
