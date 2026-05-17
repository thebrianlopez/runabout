package api

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/ctreminiom/go-atlassian/v2/pkg/infra/models"

	"github.com/thebrianlopez/runabout/cmd/workctl/internal/config"
	localModels "github.com/thebrianlopez/runabout/cmd/workctl/internal/models"
)

// GetAllConfluenceArticles retrieves all Confluence pages contributed to by a user within a date range
func (a *AtlassianClients) GetAllConfluenceArticles(accountID string, cfg *localModels.QueryConfig) ([]localModels.ConfluenceArticle, error) {
	// Use configured type or default to "page"
	contentType := cfg.ConfluenceType
	if contentType == "" {
		contentType = "page"
	}

	cql := fmt.Sprintf(`type in (%s) AND (lastmodified >= "%s" AND lastmodified < "%s") AND contributor="%s"`,
		contentType,
		cfg.StartDate,
		cfg.EndDate,
		accountID)

	if config.Debug {
		config.LogDebug("Fetching all articles with CQL: %s", cql)
	}

	var result *models.SearchPageScheme
	var response *models.ResponseScheme

	options := &models.SearchContentOptions{
		Limit:  1000,
		Start:  0,
		Expand: []string{"body.storage", "history"},
	}

	err := a.withRateLimitAndRetry(func() error {
		ctx := context.Background()
		var err error
		result, response, err = a.Confluence.Search.Content(ctx, cql, options)
		return err
	})

	if err != nil {
		if response != nil && config.Debug {
			config.LogDebug("API returned status %d: %s", response.Code, response.Endpoint)
		}
		return nil, fmt.Errorf("failed to fetch articles: %w", err)
	}

	if config.Debug {
		config.LogDebug("Retrieved %d articles in single request", len(result.Results))
	}

	// Convert from go-atlassian models to our ConfluenceArticle struct for CSV export
	articles := make([]localModels.ConfluenceArticle, len(result.Results))
	for i, searchResult := range result.Results {
		// SearchResultScheme contains a Content field with the actual page data
		if searchResult.Content == nil {
			if config.Debug {
				config.LogDebug("Search result %d has no Content field", i)
			}
			continue
		}

		page := searchResult.Content
		articles[i] = localModels.ConfluenceArticle{
			ID:    page.ID,
			Title: page.Title,
		}

		// Extract space metadata. The Confluence Search API returns space info
		// via resultGlobalContainer (displayUrl="/spaces/KEY", title=space name).
		// Fall back to content.space for test fixtures or older API responses.
		if c := searchResult.ResultGlobalContainer; c != nil && c.DisplayURL != "" {
			articles[i].SpaceName = c.Title
			// displayUrl format: "/spaces/KEY"
			if parts := strings.Split(strings.TrimPrefix(c.DisplayURL, "/spaces/"), "/"); len(parts) > 0 && parts[0] != "" {
				articles[i].SpaceKey = parts[0]
			}
		} else if page.Space != nil {
			articles[i].SpaceKey = page.Space.Key
			articles[i].SpaceName = page.Space.Name
		}
		if articles[i].SpaceKey != "" {
			articles[i].URL = fmt.Sprintf("https://%s/wiki/spaces/%s/pages/%s", a.domain, articles[i].SpaceKey, page.ID)
		}

		if config.Debug {
			config.LogDebug("Processing article %d: ID=%s, Title=%s", i, page.ID, page.Title)
			config.LogDebug("  Has Body: %v, Has History: %v", page.Body != nil, page.History != nil)
			if page.History != nil {
				config.LogDebug("  History.CreatedBy: %v, CreatedDate: %s", page.History.CreatedBy != nil, page.History.CreatedDate)
			}
		}

		// Handle body content if available
		if page.Body != nil && page.Body.Storage != nil {
			articles[i].Body.Storage.Value = page.Body.Storage.Value
		}

		// Handle creator information from History
		if page.History != nil {
			if page.History.CreatedBy != nil {
				articles[i].CreatedBy.AccountID = page.History.CreatedBy.AccountID
				articles[i].CreatorAccountID = page.History.CreatedBy.AccountID
				if config.Debug {
					config.LogDebug("  CreatedBy.AccountID: %s", page.History.CreatedBy.AccountID)
				}
			}
			articles[i].CreatedDate = page.History.CreatedDate
		}
	}

	return articles, nil
}

// GetAllPagesBySpaces retrieves all pages in specified spaces within a date range
func (a *AtlassianClients) GetAllPagesBySpaces(cfg *localModels.QueryConfig) ([]localModels.ConfluenceArticle, error) {
	// Use configured type or default to "page"
	contentType := cfg.ConfluenceType
	if contentType == "" {
		contentType = "page"
	}

	// Build CQL: type in (page) AND space in (KEY1, KEY2) AND lastmodified >= date
	spaceList := strings.Join(cfg.SpaceKeys, ", ")
	cql := fmt.Sprintf(`type in (%s) AND space in (%s) AND lastmodified >= "%s" AND lastmodified < "%s"`,
		contentType,
		spaceList,
		cfg.StartDate,
		cfg.EndDate)

	if config.Debug {
		config.LogDebug("Fetching pages by spaces with CQL: %s", cql)
	}

	var result *models.SearchPageScheme
	var response *models.ResponseScheme

	options := &models.SearchContentOptions{
		Limit:  1000,
		Start:  0,
		Expand: []string{"body.storage", "history", "history.lastUpdated", "space", "version"},
	}

	err := a.withRateLimitAndRetry(func() error {
		ctx := context.Background()
		var err error
		result, response, err = a.Confluence.Search.Content(ctx, cql, options)
		return err
	})

	if err != nil {
		if response != nil && config.Debug {
			config.LogDebug("API returned status %d: %s", response.Code, response.Endpoint)
		}
		return nil, fmt.Errorf("failed to fetch pages by spaces: %w", err)
	}

	if config.Debug {
		config.LogDebug("Retrieved %d pages from %d spaces", len(result.Results), len(cfg.SpaceKeys))
	}

	articles := make([]localModels.ConfluenceArticle, len(result.Results))
	for i, searchResult := range result.Results {
		if searchResult.Content == nil {
			if config.Debug {
				config.LogDebug("Search result %d has no Content field", i)
			}
			continue
		}

		page := searchResult.Content
		articles[i] = localModels.ConfluenceArticle{
			ID:    page.ID,
			Title: page.Title,
		}

		// Extract space information and construct URL
		if page.Space != nil {
			articles[i].SpaceKey = page.Space.Key
			articles[i].SpaceName = page.Space.Name
			articles[i].URL = fmt.Sprintf("https://%s/wiki/spaces/%s/pages/%s", a.domain, page.Space.Key, page.ID)
			if config.Debug {
				config.LogDebug("  Space: %s (%s)", page.Space.Key, page.Space.Name)
			}
		}

		// Extract creator information from History
		if page.History != nil {
			if page.History.CreatedBy != nil {
				articles[i].CreatedBy.AccountID = page.History.CreatedBy.AccountID
				articles[i].CreatorAccountID = page.History.CreatedBy.AccountID
				articles[i].Creator = page.History.CreatedBy.DisplayName
				articles[i].CreatorEmail = page.History.CreatedBy.Email
				if config.Debug {
					config.LogDebug("  Creator: %s (%s)", config.RedactName(page.History.CreatedBy.DisplayName), config.RedactEmail(page.History.CreatedBy.Email))
				}
			}
			articles[i].CreatedDate = page.History.CreatedDate

			// Extract last editor information
			if page.History.LastUpdated != nil {
				if page.History.LastUpdated.By != nil {
					articles[i].LastEditor = page.History.LastUpdated.By.DisplayName
					if config.Debug {
						config.LogDebug("  Last Editor: %s", config.RedactName(page.History.LastUpdated.By.DisplayName))
					}
				}
				// LastUpdated.When is a string timestamp
				if page.History.LastUpdated.When != "" {
					articles[i].LastModifiedDate = page.History.LastUpdated.When
				}
			}
		}

		// Handle body content if available
		if page.Body != nil && page.Body.Storage != nil {
			articles[i].Body.Storage.Value = page.Body.Storage.Value
		}

		if config.Debug {
			config.LogDebug("Processed page %d: ID=%s, Title=%s, Space=%s", i, page.ID, page.Title, articles[i].SpaceKey)
		}
	}

	// Phase 2: Optional hydration for complete metadata (slower but accurate)
	if cfg.ConfluenceHydrate {
		log.Printf("⚠️  --confluence-hydrate enabled: Fetching detailed metadata for %d pages (slower)", len(articles))

		ctx := context.Background()
		hydratedCount := 0

		for i := range articles {
			if articles[i].ID == "" {
				continue
			}

			// Call individual GET API for full metadata
			hydratedPage, err := a.HydratePageMetadata(ctx, articles[i].ID)
			if err != nil {
				if config.Debug {
					config.LogDebug("Failed to hydrate page %s: %v", articles[i].ID, err)
				}
				// Continue with partial data rather than failing entirely
				continue
			}

			// Extract Creator information from hydrated response
			if hydratedPage.History != nil {
				if hydratedPage.History.CreatedBy != nil {
					articles[i].CreatedBy.AccountID = hydratedPage.History.CreatedBy.AccountID
					articles[i].CreatorAccountID = hydratedPage.History.CreatedBy.AccountID
					articles[i].Creator = hydratedPage.History.CreatedBy.DisplayName
					articles[i].CreatorEmail = hydratedPage.History.CreatedBy.Email
					if config.Debug {
						config.LogDebug("Hydrated Creator for %s: %s (%s)", articles[i].ID, config.RedactName(hydratedPage.History.CreatedBy.DisplayName), config.RedactEmail(hydratedPage.History.CreatedBy.Email))
					}
				}

				// Extract LastEditor information from hydrated response
				if hydratedPage.History.LastUpdated != nil && hydratedPage.History.LastUpdated.By != nil {
					articles[i].LastEditor = hydratedPage.History.LastUpdated.By.DisplayName
					articles[i].LastEditorAccountID = hydratedPage.History.LastUpdated.By.AccountID
					if config.Debug {
						config.LogDebug("Hydrated LastEditor for %s: %s (AccountID: %s)", articles[i].ID, config.RedactName(hydratedPage.History.LastUpdated.By.DisplayName), hydratedPage.History.LastUpdated.By.AccountID)
					}
				}
			}

			hydratedCount++
		}

		log.Printf("✅ Successfully hydrated %d/%d pages", hydratedCount, len(articles))
	}

	return articles, nil
}

// HydratePageMetadata fetches full page metadata including Creator and LastEditor information
// This is a secondary API call after search to get complete user attribution data
func (a *AtlassianClients) HydratePageMetadata(ctx context.Context, pageID string) (*models.ContentScheme, error) {
	if pageID == "" {
		return nil, fmt.Errorf("page ID is required for hydration")
	}

	// Request expanded fields: history for creator, history.lastUpdated for last editor
	expand := []string{"history", "history.lastUpdated", "space", "version"}

	if config.Debug {
		config.LogDebug("Hydrating page %s with expand: %v", pageID, expand)
	}

	var content *models.ContentScheme
	var response *models.ResponseScheme

	err := a.withRateLimitAndRetry(func() error {
		var err error
		content, response, err = a.Confluence.Content.Get(ctx, pageID, expand, 0)
		return err
	})

	if err != nil {
		if response != nil && config.Debug {
			config.LogDebug("Hydration API returned status %d: %s", response.Code, response.Endpoint)
		}
		return nil, fmt.Errorf("failed to hydrate page %s: %w", pageID, err)
	}

	if config.Debug {
		config.LogDebug("Successfully hydrated page %s", pageID)
	}

	return content, nil
}
